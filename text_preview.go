package a13n

import (
	"encoding/json"
	"errors"
	"io"
	"iter"
)

// PreviewInterruptedError marks an incomplete provisional preview. Result and
// deliberate sequential Next remain usable; this is not a Run failure.
type PreviewInterruptedError struct {
	Reason string
	Cause  error
}

func (e *PreviewInterruptedError) Unwrap() error { return e.Cause }

func (e *PreviewInterruptedError) Error() string {
	return "text preview interrupted (" + e.Reason + "); use the authoritative Run output"
}

// Text yields root assistant chunks, not a transcript. It owns the single reader
// while iterating, including while yield is paused. Only one preview may begin.
// Break does not drain or close: defer Close. Interrupted previews stay sticky.
func (i *Interaction) Text() iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		if i == nil {
			yield("", errors.New("interaction is required"))
			return
		}
		if !i.reading.TryLock() {
			yield("", errors.New("Interaction supports one active reader"))
			return
		}
		defer i.reading.Unlock()
		if i.previewErr != nil {
			yield("", i.previewErr)
			return
		}
		if i.previewUsed {
			yield("", errors.New("Interaction supports one text preview"))
			return
		}
		i.previewUsed = true
		if i.rawUsed {
			i.previewErr = &PreviewInterruptedError{Reason: "raw_frames_already_consumed"}
			yield("", i.previewErr)
			return
		}
		preview := textPreview{active: make(map[string]bool)}
		for {
			frame, err := i.next()
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				if errors.Is(err, ErrProtocol) {
					i.previewErr = &PreviewInterruptedError{Reason: "protocol", Cause: err}
					err = i.previewErr
				}
				yield("", err)
				return
			}
			chunk, emit, err := preview.project(frame)
			if err != nil {
				i.previewErr = err
				yield("", err)
				return
			}
			if emit && !yield(chunk, nil) {
				return
			}
		}
	}
}

// A small bounded set supports interleaved messages without a transcript buffer.
// Eight simultaneous message starts are supported; exhaustion interrupts.
type textPreview struct {
	active            map[string]bool
	attempt, sequence int
	seen              bool
}

func previewInterrupted(reason string) error { return &PreviewInterruptedError{Reason: reason} }
func rawString(fields map[string]json.RawMessage, key string) (string, bool) {
	var value string
	raw, ok := fields[key]
	if !ok || json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}
func foreignEvent(event map[string]json.RawMessage) bool {
	for _, key := range []string{"subagentRunId", "parentSubagentRunId", "parentToolCallId"} {
		raw, ok := event[key]
		if ok && string(raw) != "null" && string(raw) != `""` {
			return true
		}
	}
	return false
}
func hiddenEvent(event map[string]json.RawMessage) bool {
	var metadata map[string]json.RawMessage
	if json.Unmarshal(event["metadata"], &metadata) == nil {
		var display bool
		if raw, ok := metadata["display"]; ok && json.Unmarshal(raw, &display) == nil && !display {
			return true
		}
	}
	return false
}
func (p *textPreview) project(frame ThreadFrame) (string, bool, error) {
	switch frame.(type) {
	case GapFrame:
		return "", false, previewInterrupted("gap")
	case ResetFrame:
		return "", false, previewInterrupted("reset")
	}
	var boundary BoundaryFrame
	switch f := frame.(type) {
	case DeltaFrame:
		boundary = f.BoundaryFrame
	case BoundaryFrame:
		boundary = f
	default:
		return "", false, nil
	}
	delta, isDelta := frame.(DeltaFrame)
	if !isDelta {
		// Boundaries commit already delivered deltas; they are not new deltas.
		// Late/equal boundaries neither regress nor advance preview coverage.
		if !p.seen {
			// An initial boundary supplies a coordinate, not message state or
			// reconstructed content. A following orphan remains untrustworthy.
			p.seen = true
			p.attempt, p.sequence = boundary.Attempt, boundary.Sequence
			return "", false, nil
		}
		if boundary.Attempt != p.attempt {
			return "", false, previewInterrupted("attempt_changed")
		}
		if boundary.Sequence > p.sequence {
			return "", false, previewInterrupted("sequence_discontinuity")
		}
		return "", false, nil
	}
	if p.seen {
		if boundary.Attempt != p.attempt {
			return "", false, previewInterrupted("attempt_changed")
		}
		if boundary.Sequence != p.sequence+1 {
			return "", false, previewInterrupted("sequence_discontinuity")
		}
	}
	p.seen = true
	p.attempt, p.sequence = boundary.Attempt, boundary.Sequence
	event := delta.Event
	if foreignEvent(event) {
		return "", false, nil
	}
	kind, _ := rawString(event, "type")
	id, _ := rawString(event, "messageId")
	hidden := hiddenEvent(event)
	if hidden && kind != "TEXT_MESSAGE_START" && kind != "TEXT_MESSAGE_END" {
		return "", false, nil
	}
	switch kind {
	case "TEXT_MESSAGE_START":
		role, ok := rawString(event, "role")
		if id == "" || (!ok && !hidden) {
			if hidden {
				return "", false, nil
			}
			return "", false, previewInterrupted("ambiguous_start")
		}
		if _, exists := p.active[id]; exists {
			return "", false, previewInterrupted("duplicate_start")
		}
		if len(p.active) >= 8 {
			return "", false, previewInterrupted("active_message_limit")
		}
		// Suppressed starts remain bounded identities until END; producers need
		// not repeat display:false on every content frame.
		p.active[id] = role == "assistant" && !hidden
	case "TEXT_MESSAGE_CONTENT":
		assistant, exists := p.active[id]
		if id == "" || !exists {
			return "", false, previewInterrupted("missing_start")
		}
		if !assistant {
			return "", false, nil
		}
		text, ok := rawString(event, "delta")
		if !ok {
			return "", false, previewInterrupted("non_string_content")
		}
		return text, true, nil
	case "TEXT_MESSAGE_END":
		if id == "" {
			if hidden {
				return "", false, nil
			}
			return "", false, previewInterrupted("ambiguous_end")
		}
		if _, exists := p.active[id]; !exists {
			if hidden {
				return "", false, nil
			}
			return "", false, previewInterrupted("missing_start")
		}
		delete(p.active, id)
	case "CUSTOM":
		name, _ := rawString(event, "name")
		// Framing hides original CUSTOM semantics, not standard text chunks.
		// Conservatively interrupt unsupported root framing without inspecting data.
		if name == "a13n.stream.fragment" {
			return "", false, previewInterrupted("fragment")
		}
	}
	return "", false, nil
}
