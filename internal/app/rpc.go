package app

import (
	"encoding/json"
	"fmt"
	"reflect"
)

var errType = reflect.TypeOf((*error)(nil)).Elem()

// Call invokes an exported Service method by name with JSON-encoded positional arguments.
// It is the web server's counterpart of the Wails binding.
func (s *Service) Call(method string, args []json.RawMessage) (any, error) {
	m, ok := reflect.TypeOf(s).MethodByName(method)
	if !ok || method == "Call" {
		return nil, fmt.Errorf("未知方法: %s", method)
	}
	want := m.Type.NumIn() - 1 // minus receiver
	if len(args) != want {
		return nil, fmt.Errorf("%s 需要 %d 个参数，收到 %d 个", method, want, len(args))
	}
	in := make([]reflect.Value, 0, want+1)
	in = append(in, reflect.ValueOf(s))
	for i, raw := range args {
		v := reflect.New(m.Type.In(i + 1))
		if err := json.Unmarshal(raw, v.Interface()); err != nil {
			return nil, fmt.Errorf("%s 第 %d 个参数格式错误: %w", method, i+1, err)
		}
		in = append(in, v.Elem())
	}
	out := m.Func.Call(in)
	var result any
	for _, o := range out {
		if o.Type().Implements(errType) {
			if !o.IsNil() {
				return nil, o.Interface().(error)
			}
			continue
		}
		result = o.Interface()
	}
	return result, nil
}
