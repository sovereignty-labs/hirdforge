package workflows

import (
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const TaskQueue = "sovereign-session"

// SovereignMessage is sent from the gateway when Kit types in Comms
type SovereignMessage struct {
	Content   string `json:"content"`
	SessionID string `json:"session_id"`
	AgentName string `json:"agent_name"`
}

// SovereignResponse is returned to the gateway after processing
type SovereignResponse struct {
	Content string `json:"content"`
	Error   string `json:"error,omitempty"`
}

// SessionState tracks the conversation state within a workflow
type SessionState struct {
	AgentName    string             `json:"agent_name"`
	History      []SovereignMessage `json:"history"`
	LastResponse string             `json:"last_response"`
	MessageCount int                `json:"message_count"`
}

// SovereignSessionWorkflow is a long-running workflow that represents a conversation
// between the Sovereign and an agent. Kit's messages arrive as Updates, the workflow
// calls agent activities, and returns the response synchronously through the Update.
//
// Core design principle: scope can never increase without Sovereign approval.
func SovereignSessionWorkflow(ctx workflow.Context, agentName string) error {
	logger := workflow.GetLogger(ctx)
	logger.Info("SovereignSession started", "agent", agentName)

	state := &SessionState{
		AgentName: agentName,
		History:   []SovereignMessage{},
	}

	// Register the send_message Update handler.
	// This is synchronous from the gateway's perspective:
	// gateway sends Update → workflow processes → gateway gets response.
	err := workflow.SetUpdateHandler(ctx, "send_message", func(ctx workflow.Context, msg SovereignMessage) (SovereignResponse, error) {
		logger.Info("Received message", "agent", agentName, "message_num", state.MessageCount+1)

		// Configure activity options with timeout and retry
		actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: 3 * time.Minute,
			HeartbeatTimeout:    30 * time.Second,
			RetryPolicy: &temporal.RetryPolicy{
				InitialInterval:    time.Second,
				BackoffCoefficient: 2.0,
				MaximumInterval:    30 * time.Second,
				MaximumAttempts:    2,
			},
		})

		// Send message to agent and get response
		var response string
		err := workflow.ExecuteActivity(actCtx,
			(*AgentActivities).SendMessageToAgent,
			msg.AgentName,
			msg.Content,
			msg.SessionID,
		).Get(ctx, &response)

		state.MessageCount++
		state.History = append(state.History, msg)

		if err != nil {
			errMsg := fmt.Sprintf("Agent %s failed to respond: %v", agentName, err)
			logger.Error("Activity failed", "error", err)
			state.LastResponse = errMsg
			return SovereignResponse{Error: errMsg}, nil
		}

		state.LastResponse = response
		logger.Info("Response delivered", "agent", agentName, "response_len", len(response))

		return SovereignResponse{Content: response}, nil
	})
	if err != nil {
		return fmt.Errorf("register send_message handler: %w", err)
	}

	// Register query handler for state inspection (debugging, Temporal UI)
	err = workflow.SetQueryHandler(ctx, "get_state", func() (*SessionState, error) {
		return state, nil
	})
	if err != nil {
		return fmt.Errorf("register get_state query: %w", err)
	}

	// Keep workflow alive until explicitly ended.
	// The workflow lives as long as the conversation does.
	sigCh := workflow.GetSignalChannel(ctx, "end_session")
	sigCh.Receive(ctx, nil)

	logger.Info("SovereignSession ended", "agent", agentName, "total_messages", state.MessageCount)
	return nil
}