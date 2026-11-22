// Package llm talks to the Claude API: a cheap topic guard and the answer stream.
package llm

import (
	"context"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Turn is one past exchange used as context.
type Turn struct {
	Question string
	Answer   string
}

// Usage reports token consumption.
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// Add accumulates another usage.
func (u *Usage) Add(o Usage) { u.InputTokens += o.InputTokens; u.OutputTokens += o.OutputTokens }

// Client is the interface the HTTP layer depends on (faked in tests).
type Client interface {
	// Allowed reports whether the message is about the owner and safe to answer.
	Allowed(ctx context.Context, history []Turn, message string) (bool, Usage, error)
	// Stream generates the answer, calling onDelta for every text chunk.
	Stream(ctx context.Context, history []Turn, message string, onDelta func(string) error) (Usage, error)
}

// Anthropic implements Client using the Claude API.
type Anthropic struct {
	client          anthropic.Client
	model           anthropic.Model
	maxOutputTokens int64
	systemPrompt    string
}

// NewAnthropic builds a client. knowledge is embedded into the answer prompt.
func NewAnthropic(apiKey, model, knowledge string, maxOutputTokens int) *Anthropic {
	return &Anthropic{
		client:          anthropic.NewClient(option.WithAPIKey(apiKey), option.WithMaxRetries(2)),
		model:           anthropic.Model(model),
		maxOutputTokens: int64(maxOutputTokens),
		systemPrompt:    AnswerPrompt(knowledge),
	}
}

// Model returns the model id in use.
func (a *Anthropic) Model() string { return string(a.model) }

func (a *Anthropic) Allowed(ctx context.Context, history []Turn, message string) (bool, Usage, error) {
	resp, err := a.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     a.model,
		MaxTokens: 8,
		System:    []anthropic.TextBlockParam{{Text: GuardPrompt}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(GuardInput(history, message))),
		},
	})
	if err != nil {
		return false, Usage{}, fmt.Errorf("guard: %w", err)
	}
	u := Usage{InputTokens: int(resp.Usage.InputTokens), OutputTokens: int(resp.Usage.OutputTokens)}
	var sb strings.Builder
	for _, b := range resp.Content {
		if t, ok := b.AsAny().(anthropic.TextBlock); ok {
			sb.WriteString(t.Text)
		}
	}
	// Fail closed: anything other than exactly ALLOW is a refusal.
	return strings.EqualFold(strings.TrimSpace(sb.String()), "ALLOW"), u, nil
}

func (a *Anthropic) Stream(ctx context.Context, history []Turn, message string, onDelta func(string) error) (Usage, error) {
	msgs := make([]anthropic.MessageParam, 0, len(history)*2+1)
	for _, t := range history {
		msgs = append(msgs,
			anthropic.NewUserMessage(anthropic.NewTextBlock(t.Question)),
			anthropic.NewAssistantMessage(anthropic.NewTextBlock(t.Answer)),
		)
	}
	msgs = append(msgs, anthropic.NewUserMessage(anthropic.NewTextBlock(message)))

	stream := a.client.Messages.NewStreaming(ctx, anthropic.MessageNewParams{
		Model:     a.model,
		MaxTokens: a.maxOutputTokens,
		System:    []anthropic.TextBlockParam{{Text: a.systemPrompt}},
		Messages:  msgs,
	})
	defer stream.Close()

	var u Usage
	for stream.Next() {
		switch ev := stream.Current().AsAny().(type) {
		case anthropic.MessageStartEvent:
			u.InputTokens = int(ev.Message.Usage.InputTokens)
		case anthropic.MessageDeltaEvent:
			u.OutputTokens = int(ev.Usage.OutputTokens)
		case anthropic.ContentBlockDeltaEvent:
			if d, ok := ev.Delta.AsAny().(anthropic.TextDelta); ok && d.Text != "" {
				if err := onDelta(d.Text); err != nil {
					return u, err
				}
			}
		}
	}
	if err := stream.Err(); err != nil {
		return u, fmt.Errorf("stream: %w", err)
	}
	return u, nil
}
