package remoteconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"slices"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/hwq830/zeusai/server/internal/apperr"
)

var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)+$`)

// MaxRules bounds how many rules one key may carry; evaluation is linear in it.
const MaxRules = 50

// MaxValueBytes bounds one value so a user's whole config stays small.
const MaxValueBytes = 8 << 10

// ValidateDefinition checks the parts of a key fixed at creation.
func ValidateDefinition(key, namespace, typ, applyMode string) error {
	if !keyPattern.MatchString(key) || len(key) > 100 {
		return apperr.Invalid("配置项 key 只能用小写字母、数字、下划线，用点分段，如 report.create_enabled")
	}
	if !slices.Contains(Namespaces, namespace) {
		return apperr.Invalid("未知的命名空间：" + namespace)
	}
	if !slices.Contains(Types, typ) {
		return apperr.Invalid("未知的类型：" + typ)
	}
	if applyMode != ApplyImmediate && applyMode != ApplyNextLaunch {
		return apperr.Invalid("生效时机只能是 immediate 或 next_launch")
	}
	return nil
}

// CompileSchema parses a JSON Schema for a json-typed key. nil schema means "any JSON".
func CompileSchema(schema json.RawMessage) (*jsonschema.Schema, error) {
	if len(schema) == 0 || string(schema) == "null" {
		return nil, nil
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema))
	if err != nil {
		return nil, apperr.Invalid("JSON Schema 格式不正确").WithCause(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("value.schema.json", doc); err != nil {
		return nil, apperr.Invalid("JSON Schema 格式不正确").WithCause(err)
	}
	s, err := c.Compile("value.schema.json")
	if err != nil {
		return nil, apperr.Invalid("JSON Schema 不能编译：" + err.Error())
	}
	return s, nil
}

// ValidateValue checks that v fits the key's type (and schema for json keys). Endpoint keys
// must hold an https URL or an empty string.
func ValidateValue(namespace, typ string, schema *jsonschema.Schema, v json.RawMessage, where string) error {
	if len(v) == 0 {
		return apperr.Invalid(where + "缺少值")
	}
	if len(v) > MaxValueBytes {
		return apperr.Invalid(fmt.Sprintf("%s的值超过 %d KB", where, MaxValueBytes>>10))
	}
	bad := func(want string) error { return apperr.Invalid(where + "的值必须是" + want) }
	switch typ {
	case TypeBool:
		var b bool
		if json.Unmarshal(v, &b) != nil {
			return bad("布尔值 true / false")
		}
	case TypeInt:
		var f float64
		if json.Unmarshal(v, &f) != nil || f != math.Trunc(f) || math.Abs(f) > 1<<53 {
			return bad("整数")
		}
	case TypeFloat:
		var f float64
		if json.Unmarshal(v, &f) != nil {
			return bad("数字")
		}
	case TypeString:
		var s string
		if json.Unmarshal(v, &s) != nil {
			return bad("字符串")
		}
		if namespace == NSEndpoint && s != "" {
			u, err := url.Parse(s)
			if err != nil || u.Scheme != "https" || u.Host == "" {
				return bad(" https:// 开头的地址")
			}
		}
	case TypeJSON:
		inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(v))
		if err != nil {
			return bad("合法的 JSON")
		}
		if schema != nil {
			if err := schema.Validate(inst); err != nil {
				return apperr.Invalid(where + "的值不符合 JSON Schema：" + err.Error())
			}
		}
	}
	return nil
}

// ValidateKey checks a whole key: default, every rule's value and every condition.
func ValidateKey(name string, k Key, schema json.RawMessage) error {
	sch, err := CompileSchema(schema)
	if err != nil {
		return err
	}
	if err := ValidateValue(k.Namespace, k.Type, sch, k.Default, name+" 默认值"); err != nil {
		return err
	}
	if len(k.Rules) > MaxRules {
		return apperr.Invalid(fmt.Sprintf("%s 的规则超过 %d 条", name, MaxRules))
	}
	ids := map[string]bool{}
	for i, r := range k.Rules {
		where := fmt.Sprintf("%s 第 %d 条规则", name, i+1)
		if r.ID == "" || ids[r.ID] {
			return apperr.Invalid(where + "缺少 id 或 id 重复")
		}
		ids[r.ID] = true
		for _, c := range r.When {
			if err := validateCondition(c, where); err != nil {
				return err
			}
		}
		switch {
		case r.Rollout != nil && len(r.Value) > 0:
			return apperr.Invalid(where + "不能同时设置 value 和 rollout")
		case r.Rollout != nil:
			if r.Rollout.Percent < 0 || r.Rollout.Percent > 100 {
				return apperr.Invalid(where + "的放量比例必须在 0–100 之间")
			}
			if err := ValidateValue(k.Namespace, k.Type, sch, r.Rollout.Value, where); err != nil {
				return err
			}
		default:
			if err := ValidateValue(k.Namespace, k.Type, sch, r.Value, where); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateCondition(c Condition, where string) error {
	if !slices.Contains(Attrs, c.Attr) {
		return apperr.Invalid(where + "使用了未知的条件：" + c.Attr)
	}
	if !slices.Contains(Ops, c.Op) {
		return apperr.Invalid(where + "使用了未知的运算：" + c.Op)
	}
	if len(c.Values) == 0 {
		return apperr.Invalid(where + "的条件缺少取值")
	}
	if len(c.Values) > 10000 {
		return apperr.Invalid(where + "的名单超过 10000 项")
	}
	switch c.Op {
	case OpSemverGte, OpSemverLt:
		if c.Attr != AttrAppVersion && c.Attr != AttrOSVersion {
			return apperr.Invalid(where + "：版本比较只能用于 app_version 或 os_version")
		}
		if _, ok := versionParts(c.Values[0]); !ok {
			return apperr.Invalid(where + "：版本号格式应为 1.2.3")
		}
	case OpBefore, OpAfter:
		if c.Attr != AttrRegisteredAt {
			return apperr.Invalid(where + "：早于 / 晚于只能用于 registered_at")
		}
		if _, ok := parseTime(c.Values[0]); !ok {
			return apperr.Invalid(where + "：日期格式应为 2026-10-24")
		}
	default:
		if c.Attr == AttrRegisteredAt {
			return apperr.Invalid(where + "：registered_at 只能用早于 / 晚于")
		}
	}
	return nil
}
