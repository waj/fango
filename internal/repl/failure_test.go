package repl

import (
	"strings"
	"testing"
)

func TestFailureInspectionPreservesREPLGenerations(t *testing.T) {
	var out strings.Builder
	Run(strings.NewReader(`import Fail
import Failure
import Runtime.Scope
import Result exposing (Result(..))
type Token = Token Int deriving (Show)
body : () ->{Fail.Fail Token} ()
body() = Fail.fail (Token 1)
saved =
    case Fail.attemptReport { _ -> Runtime.Scope.finally body { _ -> Fail.fail (Token 2) } } of
        Err report -> report.suppressed
        Ok _ -> []
readOld : Failure.Failure -> Maybe Token
readOld failure = Failure.argument 0 failure
List.map readOld saved
type Token = Token Int deriving (Show)
readNew : Failure.Failure -> Maybe Token
readNew failure = Failure.argument 0 failure
List.map readNew saved
List.map readOld saved
:quit
`), &out)
	got := out.String()
	if strings.Contains(got, "INTERNAL") || strings.Count(got, "[Just Token 2] :") != 2 || !strings.Contains(got, "[Nothing] :") {
		t.Fatalf("failure inspection confused REPL generations:\n%s", got)
	}
}

func TestFailureReportsStageAndRollback(t *testing.T) {
	var out strings.Builder
	Run(strings.NewReader(`import Fail
import Failure
import Runtime.Scope
import Result exposing (Result(..))
import Meta
import List exposing (List(..))
message : () -> String
message() =
    result : Result (Fail.Report String) ()
    result = Fail.attemptReport { _ -> Runtime.Scope.finally { _ -> Fail.fail "body" } { _ -> Fail.fail "close" } }
    case result of
        Err report ->
            case report.suppressed of
                Cons failure _ -> Maybe.withDefault "missing" (Failure.argument 0 failure)
                Nil -> "missing"
        Ok _ -> "success"
bad : Int
bad = $(Meta.lift (message()))
:type bad
good : String
good = $(Meta.lift (message()))
good
:quit
`), &out)
	got := out.String()
	if strings.Contains(got, "INTERNAL") || !strings.Contains(got, "I don't know a value named `bad`.") || !strings.Contains(got, "close : String") {
		t.Fatalf("failure report staging or rollback failed:\n%s", got)
	}
}
