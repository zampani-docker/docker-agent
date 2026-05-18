package runtime

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/docker/docker-agent/pkg/chat"
	"github.com/docker/docker-agent/pkg/model/provider"
	"github.com/docker/docker-agent/pkg/model/provider/options"
)

// samplingHandler is the MCP-toolset-side hook that satisfies an inbound
// sampling/createMessage request from a server by driving the host agent's
// own model and returning the resulting message.
//
// The host always remains in control: the request is mapped to the agent's
// configured model (server-supplied ModelPreferences are advisory only),
// only one round-trip is performed (the model's response is returned
// verbatim, not fed back into the loop), and tool use is intentionally
// disabled — sampling is for plain text/image/audio completions, not
// nested agent runs.
func (r *LocalRuntime) samplingHandler(ctx context.Context, req *mcp.CreateMessageParams) (*mcp.CreateMessageResult, error) {
	slog.DebugContext(ctx, "Sampling request received from MCP server",
		"messages", len(req.Messages),
		"max_tokens", req.MaxTokens,
		"system_prompt", req.SystemPrompt != "",
	)

	a := r.CurrentAgent()
	if a == nil {
		return nil, errors.New("no current agent available to handle sampling request")
	}

	messages, err := samplingMessagesToChat(req)
	if err != nil {
		return nil, fmt.Errorf("converting sampling messages: %w", err)
	}

	baseModel := a.Model(ctx)
	if baseModel == nil {
		return nil, errors.New("current agent has no model configured")
	}

	model := provider.CloneWithOptions(ctx, baseModel, samplingModelOptions(req)...)

	stream, err := model.CreateChatCompletionStream(ctx, messages, nil)
	if err != nil {
		return nil, fmt.Errorf("creating sampling completion stream: %w", err)
	}

	content, finishReason, err := drainSamplingStream(stream)
	if err != nil {
		return nil, fmt.Errorf("reading sampling completion stream: %w", err)
	}

	return &mcp.CreateMessageResult{
		Role:       mcp.Role("assistant"),
		Model:      model.ID().String(),
		Content:    &mcp.TextContent{Text: content},
		StopReason: stopReason(finishReason),
	}, nil
}

// samplingMessagesToChat converts an MCP CreateMessageParams into the
// host's chat.Message slice. The optional system prompt is prepended;
// per-message Content is mapped from the supported MCP block types.
func samplingMessagesToChat(req *mcp.CreateMessageParams) ([]chat.Message, error) {
	var messages []chat.Message
	if req.SystemPrompt != "" {
		messages = append(messages, chat.Message{
			Role:    chat.MessageRoleSystem,
			Content: req.SystemPrompt,
		})
	}
	for _, m := range req.Messages {
		role, err := samplingRoleToChat(m.Role)
		if err != nil {
			return nil, err
		}
		text, parts := samplingContentToChat(m.Content)
		messages = append(messages, chat.Message{
			Role:         role,
			Content:      text,
			MultiContent: parts,
		})
	}
	if len(messages) == 0 {
		return nil, errors.New("sampling request contains no messages")
	}
	return messages, nil
}

func samplingRoleToChat(r mcp.Role) (chat.MessageRole, error) {
	switch string(r) {
	case "user":
		return chat.MessageRoleUser, nil
	case "assistant":
		return chat.MessageRoleAssistant, nil
	case "":
		// Some servers omit the role for the lone user turn; default to user
		// rather than refuse the request, matching most other MCP hosts.
		return chat.MessageRoleUser, nil
	default:
		return "", fmt.Errorf("unsupported sampling role %q", r)
	}
}

// samplingContentToChat maps a single MCP content block to the host's
// chat representation. Text blocks return a Content string; image blocks
// return a MultiContent entry with a data URL the model can consume.
// Audio blocks fall back to a textual placeholder because chat.Message
// does not currently model raw audio; this lets models acknowledge the
// attachment instead of failing the request outright.
func samplingContentToChat(c mcp.Content) (string, []chat.MessagePart) {
	switch v := c.(type) {
	case *mcp.TextContent:
		return v.Text, nil
	case *mcp.ImageContent:
		return "", []chat.MessagePart{{
			Type: chat.MessagePartTypeImageURL,
			ImageURL: &chat.MessageImageURL{
				URL: dataURL(v.MIMEType, v.Data),
			},
		}}
	case *mcp.AudioContent:
		return fmt.Sprintf("[audio attachment (%s, %d bytes) — not inlined]",
			v.MIMEType, len(v.Data)), nil
	case nil:
		return "", nil
	default:
		return fmt.Sprintf("[unsupported content type %T]", v), nil
	}
}

func dataURL(mimeType string, data []byte) string {
	mt := mimeType
	if mt == "" {
		mt = "application/octet-stream"
	}
	return "data:" + mt + ";base64," + base64.StdEncoding.EncodeToString(data)
}

// samplingModelOptions translates the server's advisory preferences into
// the host's model options. Only MaxTokens is honoured today (with an
// upper bound enforced by the underlying provider); temperature, stop
// sequences, and ModelPreferences are intentionally left to the host's
// configuration.
func samplingModelOptions(req *mcp.CreateMessageParams) []options.Opt {
	opts := []options.Opt{
		options.WithStructuredOutput(nil),
		options.WithNoThinking(),
	}
	if req.MaxTokens > 0 {
		opts = append(opts, options.WithMaxTokens(req.MaxTokens))
	}
	return opts
}

// drainSamplingStream reads a chat completion stream to completion and
// returns the concatenated assistant content alongside the final finish
// reason. The stream is always closed before returning.
func drainSamplingStream(stream chat.MessageStream) (string, chat.FinishReason, error) {
	defer stream.Close()

	var content strings.Builder
	var finishReason chat.FinishReason
	for {
		response, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return content.String(), finishReason, nil
		}
		if err != nil {
			return "", "", err
		}
		if len(response.Choices) > 0 {
			choice := response.Choices[0]
			content.WriteString(choice.Delta.Content)
			if choice.FinishReason != "" {
				finishReason = choice.FinishReason
			}
		}
	}
}

// stopReason maps a chat finish reason into the MCP stopReason vocabulary
// used in CreateMessageResult. Unknown values fall back to "endTurn",
// which is the protocol's default for a normal assistant turn.
func stopReason(fr chat.FinishReason) string {
	switch fr {
	case chat.FinishReasonStop:
		return "endTurn"
	case chat.FinishReasonLength:
		return "maxTokens"
	case chat.FinishReasonToolCalls:
		return "toolUse"
	default:
		return "endTurn"
	}
}
