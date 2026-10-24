# Contributing

[Overview](README.md) · [Verifying releases](docs/VERIFYING-RELEASES.md) · [Assurance case](docs/ASSURANCE-CASE.md)

## How changes land

- Open a pull request against `main`. `.github/workflows/ci.yml` must be green before merging.
- Read [AGENTS.md](AGENTS.md) and every AGENTS.md on the path to the files you change. They are
  binding: each lists the contracts for its subtree and the checks to run. Update the closest one
  in the same change.
- Report bugs and propose features in [GitHub issues](https://github.com/Busnes-app/kydrive-desktop/issues).
  Report vulnerabilities privately, as the organisation's
  [security policy](https://github.com/Busnes-app/.github/blob/main/SECURITY.md) explains.

## Tests

Write the failing test first and watch it fail (AGENTS.md, Work Guidance). A new engine test goes
through `setup`, so it runs against both the fake and a real server (`core/AGENTS.md`). Each
subtree's AGENTS.md names its test command; CI runs all of them.

## Coding standard

Each language follows its usual style guide. The right-hand column is what CI enforces; the rest
is review.

| Language | Style guide | Enforced by |
|---|---|---|
| Go (`core/`) | [Effective Go](https://go.dev/doc/effective_go) and the [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments) | `gofmt -l` must print nothing (Linux) and `go vet ./...` on all three OSes, in the `core` job |
| C++20 and QML (`linux/`) | [KDE Frameworks coding style](https://community.kde.org/Policies/Frameworks_Coding_Style) | `-Wall -Wextra -Wshadow -Wnon-virtual-dtor -Woverloaded-virtual` (`linux/CMakeLists.txt`), shown in the `linux-shell` job but not fatal; `tst_qml` fails on any QML warning |
| Swift (`macos/`) | [Swift API Design Guidelines](https://www.swift.org/documentation/api-design-guidelines/) | `swift test` in `macos-shell`, including `PlainTextTests` |
| C# (`windows/`) | [.NET coding conventions](https://learn.microsoft.com/dotnet/csharp/fundamentals/coding-style/coding-conventions) | Nullable reference types on in every project; `dotnet test` in `windows-shell`, including `SafetyTests` |

Beyond what the tools check: YAGNI, minimal code, short comments that say why (AGENTS.md); Go
dependencies stay within the list in AGENTS.md; shells contain no sync logic.
