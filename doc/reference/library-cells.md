# Scope-owned write-once cells

`Cell` stores a value at one fixed type, including any residual effect-row index.
A cell belongs to a `Runtime.Coroutine.scope` and can be shared by cooperative children
registered in that scope.

```fango
create : Runtime.Coroutine.Scope e ->{IO} Publisher a
createIn : Runtime.Coroutine.Facet ->{IO} Publisher a
reader : Publisher a -> Reader a
publish : Publisher a -> a ->{IO} Bool
read : Reader a ->{IO} Maybe a
```

`create` allocates an empty cell and its sole publication capability. `createIn`
uses the same checked scope identity through a hidden-row registration facet;
it supports dynamic clients such as the [cooperative task
driver](library-async-cooperative.md). `reader`
creates a read-only view; multiple views share the same readiness and value.
`publish` returns `True` for the first publication and `False` thereafter,
preserving the first value. Aliases of a publisher refer to that same write-once
capability; this API does not impose linear use of source variables. `read`
returns `Nothing` before publication and `Just value` afterwards. It never waits
or drives a producer.

Publisher and reader representations are private. Both retain their scope's
facet and cannot escape its lifetime, including inside closures or data
structures. A local allocation is monomorphic: using the same cell at Int and
String, or at two different completion rows, is rejected. There is no lookup by
an untyped task ID and readers cannot publish.

Publication requires a transitively capture-free payload. A closure retaining
mutable handler state or a borrowed cursor/resource cannot be hidden in a cell.
Records, recursive ADTs, Unit, and capture-free function values can be stored;
the native implementation neither inspects nor invokes the payload. A
`Runtime.Completion.Completion a e` retains its full value and row index under these
same rules.

Native readiness and publication use a mutex. This is the cell's synchronization
contract, not permission to run Fango callbacks concurrently. Scope closure first
drains registered children through the ordinary Coroutine registry. Cells have
no background requests, callbacks, or asynchronous release obligation.
