package native

func HostNew(capacity int64) any   { return FangoNewRequestHost(capacity) }
func HostClose(host any)           { host.(*FangoRequestHost).Close() }
func Reserve(host any) any         { return host.(*FangoRequestHost).Reserve() }
func Admitted(token any) bool      { return token.(*FangoRequest).Admitted() }
func Claim(host, token any) bool   { return host.(*FangoRequestHost).Claim(token.(*FangoRequest)) }
func CancelRegistration(token any) { token.(*FangoRequest).Cancel() }
func DrainRegistration(token any)  { token.(*FangoRequest).Drain() }
func LiveCount(host any) int64     { live, _ := host.(*FangoRequestHost).Counts(); return live }
func RegistrationCount(host any) int64 {
	_, registrations := host.(*FangoRequestHost).Counts()
	return registrations
}
