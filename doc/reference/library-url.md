# URLs

Parsing, printing, and resolving RFC 3986 URI references, and encoding their
components.

[Reference index](../reference.md). Source: [Url](../../stdlib/Url.fango).

## The `Url` type

```fango
type Url =
    { scheme : Maybe String
    , userinfo : Maybe String
    , host : Maybe String
    , port : Maybe Int
    , path : String
    , query : Maybe String
    , fragment : Maybe String
    }
```

A reference may be absolute or relative: a relative one has no scheme and
possibly no host. A host is present exactly when the reference has an
authority (`//…`); `userinfo` and `port` occur only with a host.

`path`, `query`, and `fragment` keep their encoded form, without the `?` or
`#`. `query = Just ""` (a bare `?`) and `query = Nothing` stay distinct.

## Parsing and printing

`Url.parse : String -> Maybe Url` accepts RFC 3986 characters only. It returns
`Nothing` for:

- characters a component does not allow, such as a space or a non-ASCII
  character (encode them first);
- a malformed percent escape;
- a relative reference whose first path segment holds a `:`;
- a malformed host, a non-ASCII host, or a port that is not decimal or is above
  65535.

The scheme and a registered host name are lowercased. An IPv6 literal is stored
without its brackets, the form a socket dial needs. Internationalized host
names (IDNA) are not supported.

`Url.toString` prints a URL back. For anything `parse` accepted it returns the
input, except that the scheme and host are lowercased and an empty port
(`http://h:/`) is dropped. `toString` restores IPv6 brackets.

`Url.requestTarget` gives the origin-form target an HTTP client sends: the path,
`/` when empty, followed by `?query` when present. The fragment is never part
of it.

## Resolution

`Url.resolve base reference` implements RFC 3986 §5.2: a reference with a
scheme or host replaces the base's, an empty path keeps the base path, a
relative path merges with the base path's directory, and dot segments are
removed. The reference's fragment always wins.

## Encoding and queries

`Url.percentEncode component text` encodes the UTF-8 bytes of `text` that the
component does not keep literally, as `%XX` with uppercase hex:

| Component | Kept literally |
| --- | --- |
| `PathSegment` | unreserved characters, sub-delimiters, `:`, `@`; `/` is encoded |
| `QueryPart` | unreserved characters and `! $ ' ( ) * , / : ; ? @`; `&`, `=`, `+`, `#`, and spaces are encoded |
| `FragmentPart` | path characters, `/`, and `?` |

`Url.percentDecode` decodes escapes and returns `Nothing` for a malformed
escape or invalid UTF-8.

`Url.queryParams url` returns the decoded `key=value` pairs in order. It reads
`+` as a space, as HTML forms write it, skips empty parts, gives a part without
`=` an empty value, and keeps a part that fails to decode as written.
`Url.withQuery pairs url` appends encoded pairs to any existing query; spaces
become `%20`, which every server accepts. `Url.appendPath segments url`
encodes each segment, `/` included, and joins it to the path.
