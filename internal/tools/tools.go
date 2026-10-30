// Package tools implements the functions the model can call. Each tool
// validates its arguments, runs against the store or the knowledge base, and
// returns a JSON-friendly map.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AmmarBerkovic/messaging-diagnostics/internal/kb"
	"github.com/AmmarBerkovic/messaging-diagnostics/internal/llm"
	"github.com/AmmarBerkovic/messaging-diagnostics/internal/store"
)

// Tool pairs the description the model sees with the code that runs.
type Tool struct {
	Spec llm.ToolSpec
	Run  func(ctx context.Context, args map[string]any) (map[string]any, error)
}

// Registry holds the available tools in a fixed order.
type Registry struct {
	tools []Tool
}

// New returns the diagnostic tools backed by st.
func New(st *store.Store) *Registry {
	return &Registry{tools: []Tool{
		searchLogs(st),
		getMessage(st),
		lookupErrorCode(),
	}}
}

// Specs returns the tool descriptions to send to the model.
func (r *Registry) Specs() []llm.ToolSpec {
	specs := make([]llm.ToolSpec, len(r.tools))
	for i, t := range r.tools {
		specs[i] = t.Spec
	}
	return specs
}

// Call runs one tool call. Failures are returned to the model as an
// {"error": ...} result rather than aborting, so it can correct its
// arguments and try again.
func (r *Registry) Call(ctx context.Context, call llm.ToolCall) map[string]any {
	for _, t := range r.tools {
		if t.Spec.Name == call.Name {
			out, err := t.Run(ctx, call.Args)
			if err != nil {
				return map[string]any{"error": err.Error()}
			}
			return out
		}
	}
	return map[string]any{"error": fmt.Sprintf("unknown tool %q", call.Name)}
}

func searchLogs(st *store.Store) Tool {
	str := func(name, desc string, enum ...string) llm.Param {
		return llm.Param{Name: name, Type: "string", Description: desc, Enum: enum}
	}
	return Tool{
		Spec: llm.ToolSpec{
			Name: "search_logs",
			Description: "Aggregate delivery-log records matching the filters. Returns totals, failure rate, " +
				"breakdowns by status, error code, sender, country, org, recipient domain and hour, plus up to 5 " +
				"sample failed messages. All filters are optional and combine with AND.",
			Params: []llm.Param{
				str("channel", "Message channel.", "sms", "email"),
				str("provider", "Delivery provider.", "twilio", "telnyx", "smtp"),
				str("country", "Recipient country as ISO 3166 alpha-2, e.g. GB, US, CA."),
				str("org_id", "Customer organisation id, e.g. org_acme."),
				str("sender", "Exact sender: a phone number in E.164 or a from email address."),
				str("status", "Message status.", "queued", "sent", "delivered", "undelivered", "failed", "bounced"),
				str("error_code", "Provider error code, e.g. 30007 or 5.1.1."),
				str("recipient_domain", "Email recipient domain, e.g. inbox.example."),
				str("since", "Start of time range, inclusive, RFC 3339 UTC, e.g. 2026-10-28T14:00:00Z."),
				str("until", "End of time range, exclusive, RFC 3339 UTC."),
			},
		},
		Run: func(ctx context.Context, args map[string]any) (map[string]any, error) {
			f := store.Filter{
				Channel:         stringArg(args, "channel"),
				Provider:        strings.ToLower(stringArg(args, "provider")),
				Country:         strings.ToUpper(stringArg(args, "country")),
				OrgID:           stringArg(args, "org_id"),
				Sender:          stringArg(args, "sender"),
				Status:          stringArg(args, "status"),
				ErrorCode:       stringArg(args, "error_code"),
				RecipientDomain: stringArg(args, "recipient_domain"),
			}
			var err error
			if f.Since, err = timeArg(args, "since"); err != nil {
				return nil, err
			}
			if f.Until, err = timeArg(args, "until"); err != nil {
				return nil, err
			}
			sum, err := st.Search(ctx, f)
			if err != nil {
				return nil, err
			}
			return toMap(sum)
		},
	}
}

func getMessage(st *store.Store) Tool {
	return Tool{
		Spec: llm.ToolSpec{
			Name:        "get_message",
			Description: "Fetch the full delivery-log record for one message id, e.g. msg_8f2a91.",
			Params: []llm.Param{
				{Name: "message_id", Type: "string", Description: "The message id.", Required: true},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (map[string]any, error) {
			id := stringArg(args, "message_id")
			if id == "" {
				return nil, errors.New("message_id is required")
			}
			m, err := st.GetMessage(ctx, id)
			if errors.Is(err, store.ErrNotFound) {
				return map[string]any{"found": false, "message_id": id}, nil
			}
			if err != nil {
				return nil, err
			}
			out, err := toMap(m)
			if err != nil {
				return nil, err
			}
			out["found"] = true
			return out, nil
		},
	}
}

func lookupErrorCode() Tool {
	return Tool{
		Spec: llm.ToolSpec{
			Name: "lookup_error_code",
			Description: "Look up what a provider error code means, whether it is permanent, and a link to the " +
				"official documentation. Use the provider and code exactly as they appear in the logs.",
			Params: []llm.Param{
				{Name: "provider", Type: "string", Required: true, Enum: []string{"twilio", "telnyx", "smtp"},
					Description: "The provider that returned the code."},
				{Name: "code", Type: "string", Required: true,
					Description: "The error code, e.g. 30007, 40310 or 5.1.2."},
			},
		},
		Run: func(_ context.Context, args map[string]any) (map[string]any, error) {
			provider, code := stringArg(args, "provider"), stringArg(args, "code")
			if provider == "" || code == "" {
				return nil, errors.New("provider and code are required")
			}
			e, ok := kb.Lookup(provider, code)
			if !ok {
				return map[string]any{"found": false, "provider": provider, "code": code,
					"note": "code not in the knowledge base; do not guess its meaning"}, nil
			}
			out, err := toMap(e)
			if err != nil {
				return nil, err
			}
			out["found"] = true
			return out, nil
		},
	}
}

// stringArg reads an optional string argument. Models sometimes send
// numbers for codes, so non-strings are formatted rather than rejected.
func stringArg(args map[string]any, key string) string {
	switch v := args[key].(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case float64:
		return fmt.Sprintf("%g", v)
	default:
		return fmt.Sprint(v)
	}
}

func timeArg(args map[string]any, key string) (time.Time, error) {
	s := stringArg(args, key)
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must be RFC 3339, e.g. 2026-10-28T14:00:00Z: got %q", key, s)
	}
	return t, nil
}

// toMap converts a struct to the map form the model API expects, reusing
// the struct's json tags.
func toMap(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}
