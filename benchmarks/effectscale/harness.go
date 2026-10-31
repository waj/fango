package main

import (
	"fmt"
	"strings"
)

func harness(s shape) string {
	var cases strings.Builder
	for _, c := range scenarios(s) {
		if c.Kind == "go" || c.Kind == "lookup" || c.Kind == "extend" {
			continue
		}
		fmt.Fprintf(&cases, "case %q: got = V_%s(n)\n", c.ID, functionName(c))
		if checksummed(c.Kind) {
			fmt.Fprintln(&cases, "want = n*(n+1)/2")
		}
	}
	return fmt.Sprintf(harnessTemplate, s.Handlers, cases.String())
}

const harnessTemplate = `package main
import (
 "encoding/json"
 "fmt"
 "os"
 "runtime"
 "strconv"
 "strings"
 "testing"
 "time"
 "fangobuild/fangort"
)
var sink int64
var rowSink fangort.EvidenceValue
//go:noinline
func goLoop(n int64) int64 { var x int64; for i:=int64(0); i<n; i++ { x++ }; return x }
func TestMeasure(t *testing.T) {
 n, err := strconv.ParseInt(os.Getenv("STEPS"),10,64)
 if err!=nil || n<1 || n>2000000000 { t.Fatal("invalid STEPS") }
 id := os.Getenv("CASE")
 var got int64
 want := n
 var ns, bytes, allocs int64
 var bindings []*fangort.EvidenceBinding
 var row fangort.EvidenceValue
 value := int64(1)
 for i:=0; i<%d; i++ {
  binding:=&fangort.EvidenceBinding{Name:fmt.Sprintf("Effect%%d",i), Family:fangort.EvidenceFamily{Direct:&value}}
  bindings=append(bindings,binding); row=fangort.ExtendEvidenceValue(row,binding)
 }
 runtime.GC()
 switch id {
 %s
 default:
  var before, after runtime.MemStats
  runtime.ReadMemStats(&before); start:=time.Now()
  switch {
  case strings.Contains(id,"/go/"): got=goLoop(n)
  case strings.Contains(id,"/lookup/"):
   target:=bindings[0].Name
   if strings.Contains(id,"/inner/") { target=bindings[len(bindings)-1].Name }
   for i:=int64(0); i<n; i++ { got+=*fangort.ValueEvidence[*int64](row,target,fangort.DirectEvidence) }
  case strings.Contains(id,"/extend/"):
   for i:=int64(0); i<n; i++ { rowSink=fangort.ExtendEvidenceValue(fangort.EvidenceValue{},bindings...); got++ }
  default: t.Fatalf("unknown case %%s",id)
  }
  ns=time.Since(start).Nanoseconds(); runtime.ReadMemStats(&after)
  bytes=int64(after.TotalAlloc-before.TotalAlloc); allocs=int64(after.Mallocs-before.Mallocs)
 }
 if ns==0 { ns=V_readMetric(0); bytes=V_readMetric(1); allocs=V_readMetric(2) }
 if got!=want { t.Fatalf("checksum %%d, want %%d",got,want) }
 sink=got
 data,err:=json.Marshal(struct{ NS, Bytes, Allocs, Steps, Checksum int64 }{ns,bytes,allocs,n,got})
 if err!=nil { t.Fatal(err) }; fmt.Printf("MEASURE %%s\n",data)
}
`
