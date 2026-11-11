---
summary: "A sample doc: every construct the converter handles, with `code` and a {placeholder}."
read_when: You are testing docs-to-adoc and need one doc that uses every Markdown construct our docs do.
owns: the converter's sample
prereqs: other.md
tier: leaf
updated: 2026-11-11
---

# Sample doc (`docs-to-adoc`)

A paragraph with **bold**, *italic*, _also italic_, and `inline code`.
It keeps its semantic line breaks,
links to [the other doc](other.md), to [a section of it](other.md#the-section),
to [this one's](#lists-and-code) own section, to [a usage doc](usage/u.md#usage-heading),
to [a design record](superpowers/s.md), to [the code](../internal/foo.go),
and to [Go](https://go.dev). A bare <https://example.com> autolink too.
Some text says `{Targets, Timeout}`, `a*b*c`, `x_y_z`, `--flag`, `a -> b` and `jam`'s.
Intra**word** bold and `code`-adjacent text, and a placeholder {name} in prose.
A hard break ends this line  
and an escaped \*star\* stays literal.

## Lists and code

- one
- two, with a nested list:
  1. first
  2. second
     continued lazily
- three, with a code block:

  ```go
  func main() {}
  ```

- [ ] a task

```bash
just test
```

```
plain listing
```

## Tables

| Doc | Read when |
|-----|:---------:|
| [other.md](other.md) | You need `a\|b` or **this**. |
| u | right |

## Quotes and callouts

> **Note:** a callout
> on two lines.

> [!WARNING]
> GitHub's form.

> A plain quote.

---

![A picture](images/pic.png)
