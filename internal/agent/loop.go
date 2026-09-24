// Copyright (c) 2026 PawAgents Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package agent

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pawagents/pawagents/internal/apperrors"
	"github.com/pawagents/pawagents/internal/llm"
	"github.com/pawagents/pawagents/internal/tools"
)

// structuredSchemaName labels the response schema for providers that require a
// schema name, and for the error a provider raises when it cannot honour it.
const structuredSchemaName = "pawagents_result"

// toolOutputTruncation is appended to a tool result the loop cut to fit the
// per-call budget.
const toolOutputTruncation = "\n... [tool output truncated at the agent budget]"

// RunnerOptions is everything a runner needs. The orchestrator builds it.
type RunnerOptions struct {
	// Profile is the agent profile.
	Profile Profile
	// Model is the provider to call.
	Model Model
	// ModelName is the concrete model name.
	ModelName string
	// Capabilities is the effective capability set, overrides applied.
	Capabilities llm.ModelCapabilities
	// Executor runs the granted tools. It may be nil when the agent has none.
	Executor *tools.Executor
	// Budget bounds the task.
	Budget Budget
	// MaxOutputTokens is the completion budget of one round.
	MaxOutputTokens int
	// Temperature is the model sampling temperature, when configured.
	Temperature *float64
	// Compactor reduces a conversation that outgrows the context window. Nil
	// selects the built-in elision strategy.
	Compactor Compactor
	// Recorder receives the events of the task, for example a session record.
	// Nil means the task is not recorded.
	Recorder Recorder
	// Logger receives loop diagnostics.
	Logger *slog.Logger
}

// Recorder receives the events of one task.
//
// The loop defines the interface it depends on, so that recording a task does
// not make the agent package depend on a store, a file format or a session.
// Every call is advisory: the runtime keeps working when a recorder fails or is
// absent.
type Recorder interface {
	// Message records one conversation turn.
	Message(message llm.Message)
	// ToolCall records one tool call and its result.
	ToolCall(call llm.ToolCall, result llm.ToolResult)
	// Compaction records that the conversation was reduced.
	Compaction(report CompactionReport)
}

// Runner executes one delegated task against one model.
type Runner struct {
	options     RunnerOptions
	definitions []llm.ToolDefinition
	logger      *slog.Logger

	// hasTools reports whether any tool was granted.
	hasTools bool
	// structured reports whether the answer must be the finding envelope.
	structured bool
	// enforceSchema reports whether the provider can constrain the answer to
	// a JSON schema, as opposed to being asked for JSON in the prompt.
	enforceSchema bool
	// compactor reduces the conversation when it outgrows the window.
	compactor Compactor
	// recorder receives the events of the task, never nil.
	recorder Recorder
	// compactionSummary is the note left by the last compaction. It is part of
	// the system prompt, so that every later request carries it.
	compactionSummary string
}

// NewRunner validates the agent against the model and builds a runner.
//
// Capability validation happens here, once, before any token is spent: an agent
// that grants tools cannot run on a model without tool calling, and pretending
// otherwise by describing tools in the prompt would be exactly the silent
// degradation the specification forbids.
func NewRunner(options RunnerOptions) (*Runner, error) {
	if options.Model == nil {
		return nil, apperrors.New(apperrors.KindInternal, "agent.runner",
			"the runner needs a model")
	}
	if strings.TrimSpace(options.ModelName) == "" {
		return nil, apperrors.New(apperrors.KindConfig, "agent.runner",
			"the runner needs a concrete model name")
	}

	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}

	runner := &Runner{
		options:       options,
		logger:        logger,
		hasTools:      options.Profile.Grant != nil && !options.Profile.Grant.Empty(),
		structured:    options.Profile.OutputMode != OutputText,
		enforceSchema: false,
		compactor:     options.Compactor,
		recorder:      options.Recorder,
	}
	if runner.compactor == nil {
		runner.compactor = ElisionCompactor{}
	}
	if runner.recorder == nil {
		runner.recorder = nopRecorder{}
	}

	if runner.structured {
		// Structured output is a request, not a requirement: the runtime asks
		// for a schema when the provider can enforce one, and otherwise
		// instructs the model in the prompt. Requiring it would exclude every
		// local model that cannot enforce a schema while still answering in
		// JSON.
		runner.enforceSchema = options.Capabilities.StructuredOutput
	}

	if runner.hasTools {
		definitions, err := options.Executor.Definitions()
		if err != nil {
			return nil, err
		}
		runner.definitions = definitions
	}

	if err := runner.validateCapabilities(); err != nil {
		return nil, err
	}

	return runner, nil
}

// validateCapabilities refuses to run when the model cannot do what the agent
// needs.
func (r *Runner) validateCapabilities() error {
	requirements := llm.Requirements{
		Tools:            r.hasTools,
		SystemMessage:    strings.TrimSpace(r.options.Profile.SystemPrompt) != "",
		MaxOutputTokens:  r.options.MaxOutputTokens,
		MinContextTokens: r.options.Budget.MaxContextTokens,
	}
	return r.options.Capabilities.Validate(requirements, r.options.ModelName)
}

// Run executes a task and always returns a result, even when the task failed:
// a host needs the envelope, the usage and the error message together.
func (r *Runner) Run(ctx context.Context, task Task) (*Result, error) {
	started := time.Now()

	result := &Result{
		Status:    StatusCompleted,
		Agent:     r.options.Profile.Name,
		Provider:  r.options.Model.Name(),
		Model:     r.options.ModelName,
		StartedAt: started,
	}

	if task.IsEmpty() {
		result.Status = StatusFailed
		result.Error = "the task is empty"
		result.FinishedAt = time.Now()
		return result, apperrors.New(apperrors.KindInvalidArgument, "agent.run",
			"the delegated task is empty")
	}

	taskCtx := ctx
	cancel := func() {}
	if r.options.Budget.Timeout > 0 {
		taskCtx, cancel = context.WithTimeout(ctx, r.options.Budget.Timeout)
	}
	defer cancel()

	answer, usage, err := r.loop(taskCtx, task)
	result.Usage = usage
	result.FinishedAt = time.Now()

	if err != nil {
		result.Status = statusForError(err)
		result.Error = err.Error()
		return result, err
	}

	result.Text = answer
	r.applyAnswer(result, answer)
	return result, nil
}

// loop runs the model and tool cycle until the model answers without asking for
// a tool, or a budget stops it.
func (r *Runner) loop(ctx context.Context, task Task) (string, Usage, error) {
	messages := r.initialMessages(task)
	for _, message := range messages {
		r.recorder.Message(message)
	}
	usage := Usage{}

	for round := 1; ; round++ {
		if err := ctx.Err(); err != nil {
			return "", usage, contextFailure("agent.run", err)
		}
		if round > r.options.Budget.MaxRounds {
			return "", usage, budgetExceeded("agent.run",
				"the agent reached its %d round limit", r.options.Budget.MaxRounds)
		}

		messages = r.fitContext(messages)
		request := r.buildRequest(messages)

		response, err := r.generate(ctx, request)
		if err != nil {
			return "", usage, err
		}

		usage = usage.Add(Usage{
			Rounds:       1,
			InputTokens:  response.Usage.InputTokens,
			OutputTokens: response.Usage.OutputTokens,
		})
		messages = append(messages, response.Message)
		r.recorder.Message(response.Message)

		if !response.HasToolCalls() {
			return response.Text(), usage, nil
		}

		results, err := r.executeTools(ctx, response.Message.ToolCalls, &usage)
		if err != nil {
			return "", usage, err
		}
		messages = append(messages, results...)

		if r.options.Budget.MaxInputTokens > 0 && usage.InputTokens > r.options.Budget.MaxInputTokens {
			return "", usage, budgetExceeded("agent.run",
				"the agent used %d input tokens, above its %d token limit",
				usage.InputTokens, r.options.Budget.MaxInputTokens)
		}
		if r.options.Budget.MaxOutputTokens > 0 && usage.OutputTokens > r.options.Budget.MaxOutputTokens {
			return "", usage, budgetExceeded("agent.run",
				"the agent produced %d output tokens, above its %d token limit",
				usage.OutputTokens, r.options.Budget.MaxOutputTokens)
		}
	}
}

// generate performs one model call and drains its stream.
func (r *Runner) generate(ctx context.Context, request *llm.GenerateRequest) (*llm.Response, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}

	stream, err := r.options.Model.Generate(ctx, request)
	if err != nil {
		return nil, err
	}

	response, err := llm.Collect(ctx, stream)
	if err != nil {
		return nil, err
	}

	r.logger.Debug("model answered",
		slog.String("agent", r.options.Profile.Name),
		slog.String("model", r.options.ModelName),
		slog.Bool("tool_calls", response.HasToolCalls()),
		slog.String("finish_reason", string(response.FinishReason)))

	if response.FinishReason == llm.FinishReasonContentFilter {
		return nil, apperrors.New(apperrors.KindProvider, "agent.run",
			"the provider blocked the answer with a content filter")
	}
	return response, nil
}

// executeTools runs the tools a model asked for and returns the messages that
// carry their results.
func (r *Runner) executeTools(ctx context.Context, calls []llm.ToolCall, usage *Usage) ([]llm.Message, error) {
	if r.options.Executor == nil {
		return nil, apperrors.New(apperrors.KindCapability, "agent.run",
			"the model requested a tool but the agent has no tool runtime")
	}

	messages := make([]llm.Message, 0, len(calls))
	for _, call := range calls {
		if err := ctx.Err(); err != nil {
			return nil, contextFailure("agent.run", err)
		}
		if r.options.Budget.MaxToolCalls > 0 && usage.ToolCalls >= r.options.Budget.MaxToolCalls {
			return nil, budgetExceeded("agent.run",
				"the agent reached its %d tool call limit", r.options.Budget.MaxToolCalls)
		}

		result, err := r.options.Executor.Execute(ctx, call)
		if err != nil {
			// A denied or unknown tool is not something the model can adapt
			// to: the grant is wrong, and the host has to see that.
			return nil, err
		}
		usage.ToolCalls++

		// The result is recorded before it is trimmed to the budget, so that a
		// session shows what the tool actually returned.
		r.recorder.ToolCall(call, result)
		messages = append(messages, llm.NewToolResultMessage(r.limitToolOutput(result)))
	}
	return messages, nil
}

// nopRecorder discards events when no recorder was configured.
type nopRecorder struct{}

func (nopRecorder) Message(llm.Message)                   {}
func (nopRecorder) ToolCall(llm.ToolCall, llm.ToolResult) {}
func (nopRecorder) Compaction(CompactionReport)           {}

// limitToolOutput enforces the per-call output budget.
//
// The tool runtime already truncates to the environment cap, which the
// orchestrator sets to the same number; this second check is what makes the
// budget a property of the loop rather than a promise made by whoever built the
// executor.
func (r *Runner) limitToolOutput(result llm.ToolResult) llm.ToolResult {
	limit := r.options.Budget.MaxToolOutputBytes
	if limit <= 0 || len(result.Content) <= limit {
		return result
	}

	cut := limit
	for cut > 0 && !utf8.RuneStart(result.Content[cut]) {
		cut--
	}

	result.Content = result.Content[:cut] + toolOutputTruncation
	result.Truncated = true
	return result
}

// buildRequest assembles one generation request.
func (r *Runner) buildRequest(messages []llm.Message) *llm.GenerateRequest {
	request := &llm.GenerateRequest{
		Model:       r.options.ModelName,
		Messages:    messages,
		Tools:       r.definitions,
		MaxTokens:   r.options.MaxOutputTokens,
		Temperature: r.options.Temperature,
	}

	if r.hasTools {
		request.ToolChoice = llm.ToolChoice{Mode: llm.ToolChoiceAuto}
	}

	if r.structured && r.enforceSchema {
		schema := resultSchema()
		request.ResponseSchema = &schema
		request.ResponseSchemaName = structuredSchemaName
	}

	return request
}

// initialMessages builds the conversation a task starts from.
func (r *Runner) initialMessages(task Task) []llm.Message {
	return []llm.Message{
		llm.NewSystemMessage(r.systemPrompt()),
		llm.NewUserMessage(buildUserMessage(task)),
	}
}

// systemPrompt assembles the system message, including the note a compaction
// left behind.
func (r *Runner) systemPrompt() string {
	return buildSystemMessage(r.options.Profile, promptOptions{
		hasTools:   r.hasTools,
		structured: r.structured,
	}, r.compactionSummary)
}

// fitContext compacts the conversation when it no longer fits the window.
func (r *Runner) fitContext(messages []llm.Message) []llm.Message {
	window := r.options.Budget.MaxContextTokens
	if window <= 0 {
		window = r.options.Capabilities.MaxContextTokens
	}
	if fitsContext(messages, window) {
		return messages
	}

	result, err := r.compactor.Compact(messages, CompactionOptions{Window: window})
	if err != nil {
		r.logger.Warn("context compaction failed; continuing with the full conversation",
			slog.String("agent", r.options.Profile.Name),
			slog.String("error", err.Error()))
		return messages
	}
	if !result.Report.Changed {
		return result.Messages
	}

	r.compactionSummary = result.Report.Summary
	r.recorder.Compaction(result.Report)
	r.logger.Debug("context compacted",
		slog.String("agent", r.options.Profile.Name),
		slog.Int("elided", result.Report.Elided),
		slog.Int("before_tokens", result.Report.BeforeTokens),
		slog.Int("after_tokens", result.Report.AfterTokens))

	// The system prompt carries the summary of what was compacted, so the
	// conversation itself only has to change where content was removed.
	compacted := result.Messages
	if len(compacted) > 0 && compacted[0].Role == llm.RoleSystem {
		compacted = make([]llm.Message, len(result.Messages))
		copy(compacted, result.Messages)
		compacted[0] = llm.NewSystemMessage(r.systemPrompt())
	}
	return compacted
}

// applyAnswer fills the result from the model answer.
//
// In structured mode the answer is parsed into the envelope; a model that
// framed its JSON badly still contributes its text, because a usable answer
// with imperfect framing is better than no answer at all.
func (r *Runner) applyAnswer(result *Result, answer string) {
	if !r.structured {
		result.Summary = firstParagraph(answer)
		return
	}

	parsed, ok := parseEnvelope(answer)
	if !ok {
		result.Summary = firstParagraph(answer)
		return
	}
	result.Structured = true
	result.Summary = strings.TrimSpace(parsed.Summary)
	result.Findings = parsed.Findings
}

// firstParagraph returns a short summary of a free form answer.
func firstParagraph(answer string) string {
	trimmed := strings.TrimSpace(answer)
	if trimmed == "" {
		return ""
	}
	if index := strings.Index(trimmed, "\n\n"); index > 0 {
		trimmed = trimmed[:index]
	}
	if len(trimmed) > 2000 {
		trimmed = trimmed[:2000] + "..."
	}
	return strings.TrimSpace(trimmed)
}

// statusForError maps a failure to the result status.
func statusForError(err error) string {
	switch apperrors.KindOf(err) {
	case apperrors.KindTimeout:
		return StatusTimeout
	case apperrors.KindCancelled:
		return StatusCancelled
	case apperrors.KindBudgetExceeded:
		return StatusBudgetExceeded
	default:
		return StatusFailed
	}
}

// budgetExceeded builds the error a budget failure produces.
func budgetExceeded(op, format string, args ...any) error {
	return apperrors.New(apperrors.KindBudgetExceeded, op, format, args...)
}

// contextFailure converts a context failure into the matching error kind.
func contextFailure(op string, err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return apperrors.Wrap(apperrors.KindTimeout, op, "the task deadline was exceeded", err)
	case errors.Is(err, context.Canceled):
		return apperrors.Wrap(apperrors.KindCancelled, op, "the task was cancelled", err)
	default:
		return apperrors.Wrap(apperrors.KindInternal, op, "unexpected context error", err)
	}
}
