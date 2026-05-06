package signs

import _ "embed"

// Bundled UTF-8 capable font used for all sign rendering.
//
// fpdf's built-in "Helvetica" core font is Windows-1252 only, which silently
// mojibakes any non-cp1252 characters a member might paste into a sign field
// (smart quotes, emoji, ✉/☎ glyphs from a phone keyboard, etc.) — the raw
// UTF-8 bytes get re-interpreted as cp1252, producing the classic "â€…"
// gibberish. Embedding a real Unicode TTF and registering it via
// AddUTF8FontFromBytes is the only way to get fpdf to handle the full
// Unicode range.
//
// DejaVu Sans (a Bitstream Vera derivative) is used because it is permissively
// licensed (free for any use including redistribution and modification, no
// attribution required for embedding) and ships with broad Latin / symbol
// coverage including the bullet "•" we draw and most printable Unicode.
//
//go:embed assets/DejaVuSans.ttf
var fontDejaVuSansRegular []byte

//go:embed assets/DejaVuSans-Bold.ttf
var fontDejaVuSansBold []byte

// signFontFamily is the family name we register the embedded font under.
// Any value works as long as it matches the SetFont calls in render.go;
// "sign" is short and avoids colliding with fpdf's built-in family names.
const signFontFamily = "sign"
