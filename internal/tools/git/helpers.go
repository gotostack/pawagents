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

package git

import (
	"context"
	"errors"

	"github.com/pawagents/pawagents/internal/apperrors"
)

// contextError converts a context failure into a classified error so that a
// cancelled or timed out git command is distinguishable from a git failure.
func contextError(op string, err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return apperrors.Wrap(apperrors.KindTimeout, op, "the tool call timed out", err)
	case errors.Is(err, context.Canceled):
		return apperrors.Wrap(apperrors.KindCancelled, op, "the tool call was cancelled", err)
	default:
		return apperrors.Wrap(apperrors.KindInternal, op, "unexpected context error", err)
	}
}

// missingWorkspace reports a tool runtime without a workspace guard, which
// would let a git command run against an unknown directory.
func missingWorkspace(op string) error {
	return apperrors.New(apperrors.KindInternal, op,
		"the tool runtime has no workspace guard")
}
