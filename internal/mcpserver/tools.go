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

package mcpserver

// registerTools installs every host facing tool.
//
// The three tools are registered in one place so that the contract a host sees
// is visible at a glance, and so that a second host cannot be given a different
// set of tools by accident.
func (s *Server) registerTools() {
	s.registerAgentTools()
	s.registerDelegateTool()
}
