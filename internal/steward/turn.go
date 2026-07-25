package steward

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Inferencer runs one structured completion. Injected so the turn engine is
// testable without a live model.
type Inferencer interface {
	Complete(ctx context.Context, system string, history []Turn, message string) (string, error)
}

// IssueCreator creates a labeled issue. This is the ONLY side effect available
// to a Steward turn, and it is deliberately the same path an operator uses
// (POST /cortex/dispatch → labeled issue → Gitea webhook → deterministic route),
// so a Steward-created task is routed exactly like any other.
type IssueCreator interface {
	CreateLabeledIssue(ctx context.Context, repo, title, body, label string) (Created, error)
}

// StatusReader projects the observability surface. Status answers must come from
// here, never from the model's recollection (§7).
type StatusReader interface {
	Summarize(ctx context.Context, query string) (string, error)
}

// Engine runs Steward turns.
type Engine struct {
	Infer       Inferencer
	Issues      IssueCreator
	Status      StatusReader
	Sessions    *SessionStore
	DefaultRepo string
	BuildLabel  string
}

// Result is one completed turn, mirroring the §7 chat response.
type Result struct {
	Reply   string   `json:"reply"`
	Intent  Intent   `json:"intent"`
	Created *Created `json:"created_issue,omitempty"`
}

// SystemPrompt is the Steward's operating instruction. It states the role, the
// output contract, and — critically — the limits, so the model is not even
// invited to attempt an action it cannot perform.
func (e *Engine) SystemPrompt() string {
	var b strings.Builder
	b.WriteString("You are the Steward: the conversational front door of Hirdforge, an autonomous build platform.\n\n")
	b.WriteString("Your job is to turn what the operator says into ONE of: a conversational reply, a well-formed issue for an autonomous builder, a status question to be answered from the system's records, or a single clarifying question.\n\n")
	b.WriteString("## What you can and cannot do\n\n")
	b.WriteString("You can file issues and answer questions. You CANNOT dispatch, retry, cancel, approve, or merge anything — those are the operator's actions, not yours, and you have no way to perform them. Never claim you did.\n\n")
	b.WriteString("## Writing an issue\n\n")
	b.WriteString("An autonomous builder will implement your issue without asking follow-ups, and its definition of done comes from what you write. So the body MUST state the change concretely and the acceptance criteria explicitly (what must exist, what must pass). Keep it minimal and self-contained. A vague issue produces a vague build.\n\n")
	b.WriteString("## When the request is underspecified\n\n")
	b.WriteString("Do NOT invent requirements. Ask ONE focused question (intent \"clarify\"). Guessing acceptance criteria wastes a whole build cycle.\n\n")
	b.WriteString("## Output contract\n\n")
	b.WriteString("Reply with ONE JSON object and nothing else:\n")
	b.WriteString("{\"intent\":\"chat|create_issue|status_query|clarify\",\"reply\":\"what to say to the operator\",\"issue\":{\"title\":\"...\",\"body\":\"...\"}}\n")
	b.WriteString("Include \"issue\" ONLY for intent create_issue. Use status_query when the operator asks what is happening; you will not answer it yourself — the system fills in the facts.\n")
	return b.String()
}

// Run executes one turn: model → validate → act. Any failure to produce a valid
// proposal degrades to a clarification, never to an action.
func (e *Engine) Run(ctx context.Context, sessionID, message string) (Result, error) {
	if strings.TrimSpace(message) == "" {
		return Result{}, fmt.Errorf("steward: empty message")
	}
	history := e.Sessions.History(sessionID)

	raw, err := e.Infer.Complete(ctx, e.SystemPrompt(), history, message)
	if err != nil {
		return Result{}, fmt.Errorf("steward: inference: %w", err)
	}

	prop, perr := ParseProposal(raw)
	if perr == nil {
		perr = prop.Validate()
	}
	if perr != nil {
		// The model produced something unusable. Do NOT act on a guess — say so
		// and ask, which is the same fail-open discipline the builder has.
		res := Result{
			Intent: IntentClarify,
			Reply:  "I couldn't turn that into a well-formed request. Could you restate what you want built, including how you'd know it's done?",
		}
		e.record(sessionID, message, res)
		return res, nil
	}

	res := Result{Intent: prop.Intent, Reply: prop.Reply}

	switch prop.Intent {
	case IntentStatusQuery:
		// Answer from the records, never from the model's recollection (§7).
		if e.Status != nil {
			if summary, serr := e.Status.Summarize(ctx, message); serr == nil && strings.TrimSpace(summary) != "" {
				res.Reply = summary
			} else if serr != nil {
				res.Reply = "I couldn't read the task records just now, so I won't guess: " + serr.Error()
			}
		}
	case IntentCreateIssue:
		repo := firstNonEmpty(prop.Issue.Repo, e.DefaultRepo)
		label := firstNonEmpty(prop.Issue.Label, e.BuildLabel)
		body := prop.Issue.Body
		if a := strings.TrimSpace(prop.Issue.Accept); a != "" && !strings.Contains(strings.ToLower(body), "acceptance") {
			body += "\n\nAcceptance: " + a
		}
		created, cerr := e.Issues.CreateLabeledIssue(ctx, repo, prop.Issue.Title, body, label)
		if cerr != nil {
			// Be honest about the failure rather than claiming success.
			res.Intent = IntentClarify
			res.Reply = "I drafted that but couldn't file it: " + cerr.Error()
		} else {
			res.Created = &created
			res.Reply = strings.TrimSpace(prop.Reply) + fmt.Sprintf("\n\nFiled %s#%d — a builder will pick it up.", created.Repo, created.Number)
		}
	}

	e.record(sessionID, message, res)
	return res, nil
}

func (e *Engine) record(sessionID, message string, res Result) {
	e.Sessions.Append(sessionID, Turn{
		At: time.Now(), Message: message,
		Reply: res.Reply, Intent: res.Intent, Issue: res.Created,
	})
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
