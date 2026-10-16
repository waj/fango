package native

import "github.com/waj/fango/runtime/fangort"

// The STM handler's log. Values are opaque payloads stored and returned
// without inspection.

func StmBegin() any { return fangort.BeginSTM() }
func StmRead(log, tvar any) any {
	result := log.(*fangort.STMLog).Read(tvar.(*fangort.TVar))
	return &result
}
func StmFresh(result any) bool      { return result.(*fangort.STMRead).Fresh }
func StmValue(result any) any       { return result.(*fangort.STMRead).Value }
func StmWrite(log, tvar, value any) { log.(*fangort.STMLog).Write(tvar.(*fangort.TVar), value) }
func StmNew(log, value any) any     { return log.(*fangort.STMLog).New(value) }
func StmCommit(log any) bool        { return log.(*fangort.STMLog).Commit() }
func StmAwait(context, log any) bool {
	return log.(*fangort.STMLog).Await(context.(*fangort.AsyncScope))
}
