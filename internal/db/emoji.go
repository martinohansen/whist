package db

import (
	"hash/fnv"
	"slices"
)

var emojiPool = []string{
	"🦄", "🐍", "🦁", "🐯", "🐺", "🦊", "🐼", "🐻", "🐨", "🐮",
	"🐷", "🐸", "🐵", "🦍", "🦝", "🦓", "🦒", "🦛", "🦘", "🐙",
	"🦑", "🦀", "🦞", "🦐", "🐠", "🐬", "🐳", "🐊", "🦈", "🦚",
	"🦜", "🦢", "🦩", "🦉", "🦅", "🦆", "🐧", "🐤", "🐝", "🌳",
	"🪲", "🦋", "🐞", "🐌", "🐢", "🐇", "🐿", "🦔", "🦥", "🍕",
	"🍔", "🍟", "🌭", "🍿", "🧃", "🍪", "🎂", "🍊", "⚽", "🏀",
	"🏈", "⚾", "🎾", "🏒", "🏓", "🚗", "🚕", "🚌", "🚓", "🚑",
	"🚀", "🛸", "👔", "🧤", "🧣", "👞", "🎸", "🎹", "🎺", "🎻",
	"🥁", "🎤", "🎧", "🎵", "🌙", "🌈", "🔥", "💧", "🎮", "🎯",
	"🎲", "🦖", "🦫", "🌸", "🧬", "🦕", "🐉", "🦗", "🕷", "🦂",
	"🦟", "🦠", "🌼", "🌴", "🌲", "🌺", "🌻", "🌷", "🤿", "🍳",
}

// emoji returns a deterministic emoji for the given name.
func emoji(name string) string {
	h := fnv.New32a()
	h.Write([]byte(name))
	return emojiPool[int(h.Sum32())%len(emojiPool)]
}

// Emoji is the exported alias for callers in other packages.
func Emoji(name string) string { return emoji(name) }

// EmojiPool returns a copy of the available emoji set, in order.
func EmojiPool() []string {
	out := make([]string, len(emojiPool))
	copy(out, emojiPool)
	return out
}

// IsEmojiInPool reports whether e is one of the supported emojis.
func IsEmojiInPool(e string) bool {
	return slices.Contains(emojiPool, e)
}
