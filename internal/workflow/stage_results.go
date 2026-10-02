// stage_results.go 收集 Stage 执行结果的求值与克隆助手：Checkpoint 规则使用的
// 受限输出路径查找、类型匹配与可比较值规范化，以及 Stage/Result/Checkpoint 的
// 深拷贝。IncidentWorkflow 对外暴露的一切内部切片都经此处的克隆函数复制，
// 保证冻结后的 Intent 与历史记录不可变。

package workflow

import (
	"encoding/json"
	"reflect"
	"strings"
)

func cloneExecutionStages(values []ExecutionStage) []ExecutionStage {
	result := make([]ExecutionStage, len(values))
	for index, value := range values {
		result[index] = value
		result[index].Actions = cloneIntendedActions(value.Actions)
		result[index].SuccessCriteria = cloneStrings(value.SuccessCriteria)
		result[index].CheckpointPolicy = cloneCheckpointPolicy(value.CheckpointPolicy)
	}
	return result
}

func cloneCheckpointPolicy(value CheckpointPolicy) CheckpointPolicy {
	result := value
	result.Rules = make([]CheckpointRule, len(value.Rules))
	for index, rule := range value.Rules {
		result.Rules[index] = rule
		result.Rules[index].Equals = cloneAny(rule.Equals)
	}
	return result
}

func cloneActionOutputReferences(values map[string]ActionOutputReference) map[string]ActionOutputReference {
	if values == nil {
		return nil
	}
	result := make(map[string]ActionOutputReference, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

// lookupOutputPath 按点分字段路径在输出对象中取值，路径不命中即失败。
func lookupOutputPath(output map[string]any, path string) (any, bool) {
	var current any = output
	for _, field := range strings.Split(strings.TrimSpace(path), ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[field]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

// lookupActionResultPath 解析 Action 结果信封上的受限路径：
// operation_status 或 output.<field>（问题 3 的字段口径）。
func lookupActionResultPath(result ActionResult, path string) (any, bool) {
	path = strings.TrimSpace(path)
	if path == "operation_status" {
		return result.OperationStatus, result.OperationStatus != ""
	}
	if !strings.HasPrefix(path, "output.") {
		return nil, false
	}
	return lookupOutputPath(result.Output, strings.TrimPrefix(path, "output."))
}

// matchesOutputType 判断实际值是否满足引用声明的 JSON 类型。
func matchesOutputType(value any, expected ActionOutputType) bool {
	switch expected {
	case ActionOutputString:
		_, ok := value.(string)
		return ok
	case ActionOutputNumber:
		switch value.(type) {
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, json.Number:
			return true
		}
	case ActionOutputInteger:
		switch typed := value.(type) {
		case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
			return true
		case float64:
			return typed == float64(int64(typed))
		case json.Number:
			_, err := typed.Int64()
			return err == nil
		}
	case ActionOutputBoolean:
		_, ok := value.(bool)
		return ok
	case ActionOutputObject:
		_, ok := value.(map[string]any)
		return ok
	case ActionOutputArray:
		if reflect.ValueOf(value).IsValid() {
			kind := reflect.TypeOf(value).Kind()
			return kind == reflect.Array || kind == reflect.Slice
		}
	}
	return false
}

// normalizeComparable 把各类数值统一为 float64 后再比较，避免 Go 静态类型
// 差异导致语义相等的数值不被 DeepEqual 判等。
func normalizeComparable(value any) any {
	switch typed := value.(type) {
	case json.Number:
		if number, err := typed.Float64(); err == nil {
			return number
		}
		return value
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int8:
		return float64(typed)
	case int16:
		return float64(typed)
	case int32:
		return float64(typed)
	case int64:
		return float64(typed)
	case uint:
		return float64(typed)
	case uint8:
		return float64(typed)
	case uint16:
		return float64(typed)
	case uint32:
		return float64(typed)
	case uint64:
		return float64(typed)
	default:
		return value
	}
}

func findResolvedAction(values []ResolvedAction, intentID, stageID, actionID string) *ResolvedAction {
	for index := range values {
		if values[index].IntentID == intentID && values[index].StageID == stageID && values[index].ActionID == actionID {
			return &values[index]
		}
	}
	return nil
}

func resolvedActionsForStage(values []ResolvedAction, intentID, stageID string) []ResolvedAction {
	result := make([]ResolvedAction, 0)
	for _, value := range values {
		if value.IntentID == intentID && value.StageID == stageID {
			result = append(result, value)
		}
	}
	return result
}

func findActionResult(values []ActionResult, actionID string) *ActionResult {
	for index := range values {
		if values[index].ActionID == actionID {
			return &values[index]
		}
	}
	return nil
}

func actionResultsForStage(values []ActionResult, intentID, stageID string) []ActionResult {
	result := make([]ActionResult, 0)
	for _, value := range values {
		if value.IntentID == intentID && value.StageID == stageID {
			result = append(result, cloneActionResult(value))
		}
	}
	return result
}

// 以下克隆函数保证 IncidentWorkflow 对外交界的值语义：
// 内部切片永远不会通过返回值被外部修改。
func cloneResolvedActions(values []ResolvedAction) []ResolvedAction {
	result := make([]ResolvedAction, len(values))
	for index, value := range values {
		result[index] = value
		result[index].OriginalArguments = cloneAnyMap(value.OriginalArguments)
		result[index].Arguments = cloneAnyMap(value.Arguments)
		result[index].Sources = append([]ResolvedArgumentSource(nil), value.Sources...)
	}
	return result
}

func cloneActionResult(value ActionResult) ActionResult {
	value.Output = cloneAnyMap(value.Output)
	return value
}

func cloneActionResults(values []ActionResult) []ActionResult {
	result := make([]ActionResult, len(values))
	for index := range values {
		result[index] = cloneActionResult(values[index])
	}
	return result
}

func cloneDecisionCheckpoint(value DecisionCheckpoint) DecisionCheckpoint {
	value.LatestResults = cloneActionResults(value.LatestResults)
	value.NewEvidenceRefs = cloneStrings(value.NewEvidenceRefs)
	return value
}

func cloneDecisionCheckpoints(values []DecisionCheckpoint) []DecisionCheckpoint {
	result := make([]DecisionCheckpoint, len(values))
	for index := range values {
		result[index] = cloneDecisionCheckpoint(values[index])
	}
	return result
}
