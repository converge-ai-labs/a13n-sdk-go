package a13n

import (
	"encoding/json"
	"errors"
	"github.com/converge-ai-labs/a13n-sdk-go/generated"
	"github.com/oapi-codegen/nullable"
)

// Optional preserves absent, explicit null and value without a second wire model.
type Optional[T any] = nullable.Nullable[T]

func Value[T any](v T) Optional[T] { return nullable.NewNullableWithValue(v) }
func Null[T any]() Optional[T]     { return nullable.NewNullNullable[T]() }
func Ptr[T any](v T) *T            { return &v }

// Workflow values are selected canonical wire aliases, not duplicated schemas.
type RunOptions = generated.RunOptionsInput
type Configuration = generated.RunConfigurationInput
type Overrides = generated.AgentOverrideInput
type AgentModelCharacteristics = generated.AgentModelCharacteristics
type ClientToolDefinition = generated.ClientToolDefinition
type ClientToolDefinitionPermission = generated.ClientToolDefinitionPermission
type ConnectionSelection = generated.ConnectionSelection
type ToolPermissionSetting = generated.ToolPermissionSetting
type ToolPermissionMode = generated.ToolPermissionMode

// Permission builds a canonical permission union without a numbered wrapper.
func Permission(mode ToolPermissionMode) ToolPermissionSetting {
	var value ToolPermissionSetting
	_ = value.FromToolPermissionMode(mode)
	return value
}

type MediaUnderstandingSelection = generated.MediaUnderstandingSelection
type OutputSpec = generated.OutputSpec
type OutputVariant = generated.OutputVariant
type PluginSelection = generated.PluginSelection
type RetryOverride = generated.RetryOverride
type AgentReviewer = generated.AgentReviewer
type AgentReviewerOnError = generated.AgentReviewerOnError
type AgentReviewerOnFlagged = generated.AgentReviewerOnFlagged
type ToolRiskLevel = generated.ToolRiskLevel
type ToolReviewRule = generated.ToolReviewRule
type ToolReviewRuleOnFlagged = generated.ToolReviewRuleOnFlagged
type SkillSelection = generated.SkillSelection
type BuiltinSkill = generated.AgentOverrideInputBuiltinSkills
type PlatformSelection = generated.PlatformSelection
type PlatformGroup = generated.PlatformGroup
type SkillSync = generated.SkillSync
type SharedSkillSync = generated.SharedSkillSync
type IsolatedSkillSync = generated.IsolatedSkillSync
type SubagentOverride = generated.SubagentOverrideInput
type DelegationContextPolicy = generated.DelegationContextPolicy
type DelegationContextPolicyHistory = generated.DelegationContextPolicyHistory
type DelegationContextPolicyTaskState = generated.DelegationContextPolicyTaskState
type ChildEnvironmentPolicy = generated.ChildEnvironmentPolicy
type ChildEnvironmentPolicyMode = generated.ChildEnvironmentPolicyMode
type UsageLimits = generated.UsageLimitsInput
type CostLimit = generated.UsageLimitsInput_CostLimit
type ToolsetSelection = generated.ToolsetSelection
type ToolSearch = generated.ToolSearch
type ToolSearchConfig = generated.ToolSearchConfig
type ToolSearchGroup = generated.ToolSearchGroup
type NativeToolSearchConfig = generated.NativeToolSearchConfig
type NativeToolSearchStrategy = generated.NativeToolSearchConfigStrategy
type ProxyToolSearchConfig = generated.ProxyToolSearchConfig
type UsageLimit = generated.UsageLimit
type InitialMounts = generated.InitialMounts
type MountCreate = generated.MountCreate
type EnvironmentPermissionSet = generated.EnvironmentPermissionSetInput
type EnvironmentAction = generated.EnvironmentAction
type MemoryMount = generated.MemoryMount
type MemoryAccess = generated.MemoryAccess
type MessageHistory = generated.MessageHistory
type McpHeaders = generated.McpHeaders
type Delivery = generated.Delivery
type NewThreadKind = generated.NewThreadKind
type MessageKind = generated.MessageKind
type RunStatus = generated.RunStatus
type Pending = generated.Pending
type PendingCall = generated.PendingCall
type Failure = generated.Failure
type ToolCallID = generated.ToolCallId

const (
	RunCompleted = generated.RunStatusCompleted
	RunWaiting   = generated.RunStatusWaiting
	RunFailed    = generated.RunStatusFailed
	RunCancelled = generated.RunStatusCancelled
)

// Part is an ordered input part. Constructors own its wire discriminator.
type Part struct{ value any }

func TextPart(text string) Part { return Part{generated.TextPart{Type: "text", Text: text}} }
func JSONPart(value any) Part   { return Part{generated.JsonPart{Type: "json", Value: value}} }
func URLPart(url string) Part   { return Part{generated.UrlPart{Type: "url", Url: url}} }
func AssetPart(id string) Part  { return Part{generated.AssetPart{Type: "asset", AssetId: id}} }
func (p Part) MarshalJSON() ([]byte, error) {
	if p.value == nil {
		return nil, errors.New("input part needs a constructor")
	}
	return json.Marshal(p.value)
}

// Input preserves ordering and duplicate parts; Service owns content validation.
type Input struct {
	Content []Part `json:"content"`
}

// Wire is the explicit advanced conversion to the canonical message model.
func (in Input) Wire() (generated.MessagePayload, error) {
	data, err := json.Marshal(in)
	if err != nil {
		return generated.MessagePayload{}, err
	}
	var payload generated.MessagePayload
	err = json.Unmarshal(data, &payload)
	return payload, err
}

// InputFromWire retains advanced parts without translating their content.
func InputFromWire(payload generated.MessagePayload) Input {
	in := Input{Content: make([]Part, len(payload.Content))}
	for j, p := range payload.Content {
		in.Content[j] = Part{p}
	}
	return in
}

// Answer is a closed approval or client-call result; Returned(nil) is an answer.
type Answer struct {
	kind   string
	value  any
	reason Optional[string]
}
type Answers map[string]Answer

func Approve() Answer { return Answer{kind: "approve"} }
func Deny(reason ...Optional[string]) Answer {
	a := Answer{kind: "deny"}
	if len(reason) > 1 {
		a.kind = "invalid"
	} else if len(reason) == 1 {
		a.reason = reason[0]
	}
	return a
}
func Returned(value any) Answer    { return Answer{kind: "returned", value: value} }
func Failed(message string) Answer { return Answer{kind: "failed", value: message} }

type ResumeOptions struct {
	IdempotencyKey string
	Input          Optional[Input]
}

func (answers Answers) wire(options ResumeOptions) (generated.Resume, error) {
	request := generated.Resume{Approvals: make(map[string]generated.ApprovalDecision), Calls: make(map[string]generated.CallResult)}
	for id, a := range answers {
		var value any
		switch a.kind {
		case "approve":
			value = generated.Approve{Action: "approve"}
		case "deny":
			value = generated.Deny{Action: "deny", Reason: a.reason}
		case "returned":
			value = generated.Returned{Status: "returned", Value: a.value}
		case "failed":
			value = generated.Failed{Status: "failed", Message: a.value.(string)}
		default:
			return request, errors.New("answer needs an approve, deny, returned or failed constructor")
		}
		data, err := json.Marshal(value)
		if err != nil {
			return request, err
		}
		if a.kind == "approve" || a.kind == "deny" {
			var decision generated.ApprovalDecision
			if err = json.Unmarshal(data, &decision); err != nil {
				return request, err
			}
			request.Approvals[id] = decision
		} else {
			var result generated.CallResult
			if err = json.Unmarshal(data, &result); err != nil {
				return request, err
			}
			request.Calls[id] = result
		}
	}
	if options.Input.IsNull() {
		request.Input = Null[generated.MessagePayload]()
	} else if options.Input.IsSpecified() {
		in, err := options.Input.Get()
		if err != nil {
			return request, err
		}
		payload, err := in.Wire()
		if err != nil {
			return request, err
		}
		request.Input = Value(payload)
	}
	return request, nil
}
