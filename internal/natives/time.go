package natives

import timenative "github.com/waj/fango/stdlib/Time"

func installTime(t map[string]Spec) {
	t["Time.Zone.zoneUTC"] = Spec{Arity: 1, Eval: func(_ *Runtime, _ []any) (any, error) { return timenative.ZoneUTC(), nil }}
	t["Time.Zone.zoneFixed"] = Spec{Arity: 1, Eval: func(_ *Runtime, a []any) (any, error) { return timenative.ZoneFixed(a[0].(int64)), nil }}
	t["Time.Zone.zoneLoadRaw"] = Spec{Arity: 1, Eval: func(_ *Runtime, a []any) (any, error) { return timenative.ZoneLoadRaw(a[0].(string)), nil }}
	t["Time.Zone.zoneSystem"] = Spec{Arity: 1, Eval: func(_ *Runtime, _ []any) (any, error) { return timenative.ZoneSystem(), nil }}
	t["Time.Zone.zoneLoadOK"] = Spec{Arity: 1, Eval: func(_ *Runtime, a []any) (any, error) { return timenative.ZoneLoadOK(a[0]), nil }}
	t["Time.Zone.zoneLoaded"] = Spec{Arity: 1, Eval: func(_ *Runtime, a []any) (any, error) { return timenative.ZoneLoaded(a[0]), nil }}
	t["Time.Zone.zoneIdentifier"] = Spec{Arity: 1, Eval: func(_ *Runtime, a []any) (any, error) { return timenative.ZoneIdentifier(a[0]), nil }}
	t["Time.Zone.zoneOffset"] = Spec{Arity: 2, Eval: func(_ *Runtime, a []any) (any, error) { return timenative.ZoneOffset(a[0], a[1].(int64)), nil }}
	t["Time.Zone.zoneResolve"] = Spec{Arity: 2, Eval: func(_ *Runtime, a []any) (any, error) { return timenative.ZoneResolve(a[0], a[1].(int64)), nil }}
	t["Time.Zone.zoneResolveKind"] = Spec{Arity: 1, Eval: func(_ *Runtime, a []any) (any, error) { return timenative.ZoneResolveKind(a[0]), nil }}
	t["Time.Zone.zoneEarlier"] = Spec{Arity: 1, Eval: func(_ *Runtime, a []any) (any, error) { return timenative.ZoneEarlier(a[0]), nil }}
	t["Time.Zone.zoneLater"] = Spec{Arity: 1, Eval: func(_ *Runtime, a []any) (any, error) { return timenative.ZoneLater(a[0]), nil }}
	t["Time.Zone.zoneFirst"] = Spec{Arity: 3, Eval: func(_ *Runtime, a []any) (any, error) {
		return timenative.ZoneFirst(a[0], a[1].(int64), a[2].(int64)), nil
	}}
	t["Time.Clock.clockRead"] = Spec{Arity: 1, Eval: func(_ *Runtime, _ []any) (any, error) { return timenative.ClockRead(), nil }}
	t["Time.Clock.clockSeconds"] = Spec{Arity: 1, Eval: func(_ *Runtime, a []any) (any, error) { return timenative.ClockSeconds(a[0]), nil }}
	t["Time.Clock.clockNanos"] = Spec{Arity: 1, Eval: func(_ *Runtime, a []any) (any, error) { return timenative.ClockNanos(a[0]), nil }}
}
