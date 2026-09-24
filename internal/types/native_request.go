package types

import (
	"fmt"
	"strings"
)

const NativeRegistrationName = "Runtime.NativeRequest.Registration"

// CheckNativeRequest reconstructs the retention boundary from the public
// scheme. Only the registration token crosses modules; a host never does.
func CheckNativeRequest(n *NativeInfo) (bool, error) {
	if strings.HasPrefix(n.Name, "Runtime.NativeRequest.") || n.Template != nil {
		return false, nil
	}
	rest := n.Scheme.Body
	request := false
	for i := range n.Arity {
		fn, ok := rest.(*TFun)
		if !ok {
			return false, fmt.Errorf("invalid request native arrows")
		}
		if con, ok := fn.Arg.(*TCon); ok && con.Name == NativeRegistrationName {
			if i != 0 || len(con.Args) != 0 || i >= len(n.ParamWrappers) || n.ParamWrappers[i] == nil || n.ParamWrappers[i].Result.Unique != con.Unique {
				return false, fmt.Errorf("request registration must be the first parameter and retain its canonical wrapper")
			}
			request = true
		}
		rest = fn.Ret
	}
	if con, ok := rest.(*TCon); ok && con.Name == NativeRegistrationName {
		return false, fmt.Errorf("only NativeRequest may allocate registration tokens")
	}
	if request {
		unit, ok := rest.(*TCon)
		if !ok || unit.Name != "()" || n.Effect != nil || n.Storage.Kind != "" {
			return false, fmt.Errorf("request submission must be a value native returning Unit")
		}
	}
	return request, nil
}
