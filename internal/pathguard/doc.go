// Package pathguard confines the local filesystem paths a caller may name.
//
// Two tool arguments are paths this server acts on: read's `path`, which it
// opens and returns the text of, and download's `path`, which it writes a file
// into. Both are supplied by whatever is driving the model, and the text read
// tool returns is itself labeled UNTRUSTED — so a model acting on an
// instruction embedded in one document can ask for another. Without a
// containment, `~/.ssh/id_rsa` is a file like any other: read opens it, extracts
// it as text, and hands it back in a tool result.
//
// The containment is a set of roots a resolved path must lie under. What is in
// that set is a judgement about what a person starting this server meant to
// expose, and it is deliberately narrow: the directory the server was started
// in, the OS temporary directory, wherever this server saves its own downloads,
// and whatever the operator adds by name. Everything else is refused with a
// message naming the variable that would widen it, because a containment that
// cannot be widened on purpose gets switched off by whoever hits it.
//
// Resolution happens through symlinks and the refusal is re-checked on the open
// file descriptor, because the interesting attack is not a path that names a
// secret — it is a path that named something harmless when it was checked.
package pathguard
