package common

import (
	"fmt"
	"reflect"

	"github.com/QuantumNous/new-api/common"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ============================================================================
// Tool Schema 清理 (XGrammar 兼容性)
// ============================================================================
//
// 背景:
//   vLLM 和 NVIDIA NIM 用 xgrammar 做结构化输出约束(tool calling 时强制
//   模型输出符合 JSON schema 的内容)。但 xgrammar 不支持完整的 JSON Schema
//   规范,遇到下列字段时会报错:
//     "ValueError: The provided JSON schema contains features not supported by xgrammar"
//   参考: https://github.com/vllm-project/vllm/issues/16880 (Critical)
//
//   这导致 Claude Code / Cursor / Codex 等客户端发的复杂 tool schema 无法
//   在 vLLM/NIM 后端使用,客户端报 "tool call could not be parsed"。
//
// 本函数在请求转发给上游前,递归清理 tool schema 里 xgrammar 不支持的字段。
// 通过 ChannelSettings 里的 ToolSchemaStrictXGrammar 开关控制是否启用。
// ============================================================================

// xgrammarUnsupportedKeywords 是 xgrammar 已知不支持的 JSON Schema 关键字。
// 这些字段对 tool calling 的功能影响很小(主要是约束提示),删除后不影响
// 模型理解 tool 参数的结构。
var xgrammarUnsupportedKeywords = map[string]bool{
	// 数组约束 - 最常见的触发点(Claude Code 默认会发)
	"minItems":    true,
	"maxItems":    true,
	"uniqueItems": true,
	// 数值/字符串约束
	"minLength":     true,
	"maxLength":     true,
	"minProperties": true,
	"maxProperties": true,
	// 模式匹配 - xgrammar 用 regex 引擎但不支持所有语法
	"pattern":           true,
	"patternProperties": true,
	// 引用 - xgrammar 不解析 $ref
	"$ref": true,
	"$id":  true,
	// 复合类型 - xgrammar 对 anyOf/oneOf/allOf 支持有限
	// 注意: 这些不删,因为它们对类型判断很重要,只是清理其内部的不支持字段
	// (anyOf/oneOf/allOf 本身 xgrammar 部分支持,保留比删除安全)
	// 复杂格式
	"contentEncoding":  true,
	"contentMediaType": true,
	// 其他少见字段
	"exclusiveMinimum": true,
	"exclusiveMaximum": true,
	"multipleOf":       true,
	"const":            true,
	"examples":         true,
	"format":           true, // xgrammar 不基于 format 做约束
	"title":            true, // 仅是描述性
	"readOnly":         true,
	"writeOnly":        true,
	"deprecated":       true,
	"$comment":         true,
	"definitions":      true,
	"$schema":          true,
}

// SanitizeToolSchemaForXgrammar 清理请求体里的所有 tool 定义,
// 删除 xgrammar 不支持的 JSON Schema 字段。
//
// 输入是请求体的 JSON bytes(已经过 param_override 等处理),
// 输出是清理后的 JSON bytes。如果请求体没有 tools 字段,直接返回原数据。
//
// 清理是递归的: 会深入 properties / items / anyOf / oneOf / allOf 内部
// 继续清理,确保嵌套的 schema 节点也被处理。
//
// 安全性: 只删除约束类字段(minItems/maxItems/pattern 等),保留所有
// 类型定义字段(type/properties/items/required/description/enum 等)。
// 删除约束字段不会让模型生成无效数据,只是放宽了约束。
func SanitizeToolSchemaForXgrammar(jsonData []byte) ([]byte, error) {
	// 快速检查: 没有 tools 字段就直接返回
	toolsStr := gjson.GetBytes(jsonData, "tools")
	if !toolsStr.Exists() {
		return jsonData, nil
	}
	toolsArr := toolsStr.Array()
	if len(toolsArr) == 0 {
		return jsonData, nil
	}

	if common.DebugEnabled {
		common.SysLog("sanitizing tool schema for xgrammar compatibility")
	}

	// 用递归方式处理: 把 tools 取出来,清理每个 tool 的 parameters,再写回去
	// gjson 的 Result.Value() 返回 Go 原生类型,但 []gjson.Result 需要手动转换
	toolsSlice := make([]interface{}, 0, len(toolsArr))
	for _, r := range toolsArr {
		toolsSlice = append(toolsSlice, r.Value())
	}

	modified := false
	for i, t := range toolsSlice {
		tool, ok := t.(map[string]interface{})
		if !ok {
			continue
		}
		fn, ok := tool["function"].(map[string]interface{})
		if !ok {
			continue
		}
		params, ok := fn["parameters"]
		if !ok {
			continue
		}
		cleaned := sanitizeSchemaNode(params)
		if !reflect.DeepEqual(cleaned, params) {
			fn["parameters"] = cleaned
			toolsSlice[i] = tool
			modified = true
		}
	}

	if !modified {
		return jsonData, nil
	}

	// 写回 tools 字段
	result, err := sjson.SetBytes(jsonData, "tools", toolsSlice)
	if err != nil {
		return jsonData, err
	}
	return result, nil
}

// sanitizeSchemaNode 递归清理单个 schema 节点(map[string]interface{}),
// 返回清理后的节点(如果没改动,返回原对象)。
//
// 处理逻辑:
//  1. 删除所有 xgrammarUnsupportedKeywords 里的字段
//  2. 递归清理 properties 里每个属性的 schema
//  3. 递归清理 items(可能是单个 schema 或 schema 数组)
//  4. 递归清理 anyOf/oneOf/allOf 里每个 schema
//  5. 递归清理 additionalProperties(如果是 schema 而非 bool)
func sanitizeSchemaNode(node interface{}) interface{} {
	m, ok := node.(map[string]interface{})
	if !ok {
		return node
	}

	// 复制一份,避免修改原数据
	result := make(map[string]interface{}, len(m))
	for k, v := range m {
		result[k] = v
	}

	changed := false

	// 1. 删除不支持的关键字
	for key := range result {
		if xgrammarUnsupportedKeywords[key] {
			delete(result, key)
			changed = true
		}
	}

	// 2. 递归处理 properties
	if props, ok := result["properties"]; ok {
		if propsMap, ok := props.(map[string]interface{}); ok {
			newProps := make(map[string]interface{}, len(propsMap))
			propsChanged := false
			for propName, propVal := range propsMap {
				cleaned := sanitizeSchemaNode(propVal)
				newProps[propName] = cleaned
				if !reflect.DeepEqual(cleaned, propVal) {
					propsChanged = true
				}
			}
			if propsChanged {
				result["properties"] = newProps
				changed = true
			}
		}
	}

	// 3. 递归处理 items (可能是 object 或 array)
	if items, ok := result["items"]; ok {
		switch itemsTyped := items.(type) {
		case map[string]interface{}:
			cleaned := sanitizeSchemaNode(itemsTyped)
			if !reflect.DeepEqual(cleaned, itemsTyped) {
				result["items"] = cleaned
				changed = true
			}
		case []interface{}:
			// tuple-style items (OpenAPI 风格,但 xgrammar 也不完全支持)
			newItems := make([]interface{}, len(itemsTyped))
			itemsChanged := false
			for i, it := range itemsTyped {
				cleaned := sanitizeSchemaNode(it)
				newItems[i] = cleaned
				if !reflect.DeepEqual(cleaned, it) {
					itemsChanged = true
				}
			}
			if itemsChanged {
				result["items"] = newItems
				changed = true
			}
		}
	}

	// 4. 递归处理 anyOf / oneOf / allOf
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if arr, ok := result[key].([]interface{}); ok {
			newArr := make([]interface{}, len(arr))
			arrChanged := false
			for i, item := range arr {
				cleaned := sanitizeSchemaNode(item)
				newArr[i] = cleaned
				if !reflect.DeepEqual(cleaned, item) {
					arrChanged = true
				}
			}
			if arrChanged {
				result[key] = newArr
				changed = true
			}
		}
	}

	// 5. 递归处理 additionalProperties (如果是 schema 对象)
	if ap, ok := result["additionalProperties"]; ok {
		if apMap, ok := ap.(map[string]interface{}); ok {
			cleaned := sanitizeSchemaNode(apMap)
			if !reflect.DeepEqual(cleaned, apMap) {
				result["additionalProperties"] = cleaned
				changed = true
			}
		}
	}

	// 如果没有改动,返回原对象(便于上层比较)
	if !changed {
		return node
	}
	return result
}

// HasChannelToolSchemaCleaning 检查渠道是否启用了 tool schema 清理。
// 通过 ChannelSettings.ToolSchemaXGrammarClean 字段控制(在渠道后台设置里)。
// 默认关闭(保持向后兼容),需要时手动打开。
func ShouldCleanToolSchema(info *RelayInfo) bool {
	if info == nil {
		return false
	}
	return info.ChannelSetting.ToolSchemaXGrammarClean
}

// CleanToolSchemaIfNeeded 根据渠道设置决定是否清理 tool schema。
// 在请求转发前的最后一步调用(在 ApplyParamOverride 之后)。
func CleanToolSchemaIfNeeded(jsonData []byte, info *RelayInfo) ([]byte, error) {
	if !ShouldCleanToolSchema(info) {
		return jsonData, nil
	}
	cleaned, err := SanitizeToolSchemaForXgrammar(jsonData)
	if err != nil {
		common.SysError("failed to sanitize tool schema: " + err.Error())
		return jsonData, nil // 失败时返回原数据,不阻塞请求
	}
	if common.DebugEnabled && len(cleaned) != len(jsonData) {
		common.SysLog(fmt.Sprintf("tool schema cleaned for xgrammar: %d -> %d bytes", len(jsonData), len(cleaned)))
	}
	return cleaned, nil
}
