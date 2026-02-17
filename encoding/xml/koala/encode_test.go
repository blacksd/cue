// Copyright 2025 The CUE Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package koala_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/go-quicktest/qt"

	"cuelang.org/go/cue/ast/astutil"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/encoding/xml/koala"
)

func TestEncode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cueExpr string
		wantXML string
	}{{
		name:    "Simple text-only elements",
		cueExpr: `root: {
	message: $$: "Hello"
}`,
		wantXML: `<?xml version="1.0" encoding="UTF-8"?>
<root>
	<message>Hello</message>
</root>
`,
	}, {
		name:    "Self-closing element",
		cueExpr: `root: item: {}`,
		wantXML: `<?xml version="1.0" encoding="UTF-8"?>
<root>
	<item/>
</root>
`,
	}, {
		name: "Attributes",
		cueExpr: `note: {
	$alpha: "abcd"
	$$:     "text"
}`,
		wantXML: `<?xml version="1.0" encoding="UTF-8"?>
<note alpha="abcd">text</note>
`,
	}, {
		name:    "Attributes with self-closing",
		cueExpr: `note: $alpha: "abcd"`,
		wantXML: `<?xml version="1.0" encoding="UTF-8"?>
<note alpha="abcd"/>
`,
	}, {
		name: "Attribute and element with same name",
		cueExpr: `note: {
	$alpha: "abcd"
	to: $$:      "Tove"
	from: $$:    "Jani"
	heading: $$: "Reminder"
	body: $$:    "Don't forget me this weekend!"
	alpha: $$:   "efgh"
}`,
		wantXML: `<?xml version="1.0" encoding="UTF-8"?>
<note alpha="abcd">
	<to>Tove</to>
	<from>Jani</from>
	<heading>Reminder</heading>
	<body>Don't forget me this weekend!</body>
	<alpha>efgh</alpha>
</note>
`,
	}, {
		name: "Nested structures",
		cueExpr: `root: {
	message: $$: "Hello World!"
	nested: {
		a1: $$: "one level"
		a2: b: $$: "two levels"
	}
}`,
		wantXML: `<?xml version="1.0" encoding="UTF-8"?>
<root>
	<message>Hello World!</message>
	<nested>
		<a1>one level</a1>
		<a2>
			<b>two levels</b>
		</a2>
	</nested>
</root>
`,
	}, {
		name: "Lists as repeated elements",
		cueExpr: `notes: note: [{
	$alpha: "abcd"
	$$:     "hello"
}, {
	$alpha: "abcdef"
	$$:     "goodbye"
}]`,
		wantXML: `<?xml version="1.0" encoding="UTF-8"?>
<notes>
	<note alpha="abcd">hello</note>
	<note alpha="abcdef">goodbye</note>
</notes>
`,
	}, {
		name: "Namespace declarations",
		cueExpr: `"h:table": {
	"$xmlns:h": "http://www.w3.org/TR/html4/"
	"h:tr": "h:td": [{
		$$: "Apples"
	}, {
		$$: "Bananas"
	}]
}`,
		wantXML: `<?xml version="1.0" encoding="UTF-8"?>
<h:table xmlns:h="http://www.w3.org/TR/html4/">
	<h:tr>
		<h:td>Apples</h:td>
		<h:td>Bananas</h:td>
	</h:tr>
</h:table>
`,
	}, {
		name: "Namespaced attributes",
		cueExpr: `"h:table": {
	"$xmlns:h": "http://www.w3.org/TR/html4/"
	"$xmlns:f": "http://www.w3.org/TR/html5/"
	"h:tr": "h:td": [{
		"$f:type": "fruit"
		$$:        "Apples"
	}, {
		$$: "Bananas"
	}]
}`,
		wantXML: `<?xml version="1.0" encoding="UTF-8"?>
<h:table xmlns:h="http://www.w3.org/TR/html4/" xmlns:f="http://www.w3.org/TR/html5/">
	<h:tr>
		<h:td f:type="fruit">Apples</h:td>
		<h:td>Bananas</h:td>
	</h:tr>
</h:table>
`,
	}, {
		name: "XML text escaping",
		cueExpr: `root: msg: $$: "a < b & c > d"`,
		wantXML: `<?xml version="1.0" encoding="UTF-8"?>
<root>
	<msg>a &lt; b &amp; c &gt; d</msg>
</root>
`,
	}, {
		name: "XML attribute escaping",
		cueExpr: `root: $val: "a\"b&c"`,
		wantXML: `<?xml version="1.0" encoding="UTF-8"?>
<root val="a&quot;b&amp;c"/>
`,
	}, {
		name: "Non-string scalar int",
		cueExpr: `config: count: 42`,
		wantXML: `<?xml version="1.0" encoding="UTF-8"?>
<config>
	<count>42</count>
</config>
`,
	}, {
		name: "Non-string scalar float",
		cueExpr: `config: ratio: 3.14`,
		wantXML: `<?xml version="1.0" encoding="UTF-8"?>
<config>
	<ratio>3.14</ratio>
</config>
`,
	}, {
		name: "Non-string scalar bool",
		cueExpr: `config: enabled: true`,
		wantXML: `<?xml version="1.0" encoding="UTF-8"?>
<config>
	<enabled>true</enabled>
</config>
`,
	}, {
		name: "Complex nested with collections",
		cueExpr: `books: book: [{
	title: $$:  "title"
	author: $$: "John Doe"
}, {
	title: $$:  "title2"
	author: $$: "Jane Doe"
}, {
	title: $$:  "Lord of the rings"
	author: $$: "JRR Tolkien"
	volume: [{
		title: $$:  "Fellowship"
		author: $$: "JRR Tolkien"
	}, {
		title: $$:  "Two Towers"
		author: $$: "JRR Tolkien"
	}, {
		title: $$:  "Return of the King"
		author: $$: "JRR Tolkien"
	}]
}]`,
		wantXML: `<?xml version="1.0" encoding="UTF-8"?>
<books>
	<book>
		<title>title</title>
		<author>John Doe</author>
	</book>
	<book>
		<title>title2</title>
		<author>Jane Doe</author>
	</book>
	<book>
		<title>Lord of the rings</title>
		<author>JRR Tolkien</author>
		<volume>
			<title>Fellowship</title>
			<author>JRR Tolkien</author>
		</volume>
		<volume>
			<title>Two Towers</title>
			<author>JRR Tolkien</author>
		</volume>
		<volume>
			<title>Return of the King</title>
			<author>JRR Tolkien</author>
		</volume>
	</book>
</books>
`,
	}, {
		name: "Interleaving element types",
		cueExpr: `notes: {
	note: [{
		$alpha: "abcd"
		$$:     "hello"
	}, {
		$alpha: "abcdef"
		$$:     "goodbye"
	}, {
		$alpha: "ab"
		$$:     "goodbye"
	}, {
		$$: "direct"
	}]
	book: $$: "mybook"
}`,
		wantXML: `<?xml version="1.0" encoding="UTF-8"?>
<notes>
	<note alpha="abcd">hello</note>
	<note alpha="abcdef">goodbye</note>
	<note alpha="ab">goodbye</note>
	<note>direct</note>
	<book>mybook</book>
</notes>
`,
	}, {
		name: "Text content with attribute",
		cueExpr: `note: {
	$alpha: "abcd"
	$$:     "\n\thello\n"
}`,
		wantXML: "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<note alpha=\"abcd\">\n\thello\n</note>\n",
	}}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			ctx := cuecontext.New()
			v := ctx.CompileString(test.cueExpr)
			qt.Assert(t, qt.IsNil(v.Err()))

			var buf bytes.Buffer
			enc := koala.NewEncoder(&buf)
			err := enc.Encode(v)
			qt.Assert(t, qt.IsNil(err))
			qt.Assert(t, qt.Equals(buf.String(), test.wantXML))
		})
	}
}

func TestEncodeErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cueExpr   string
		wantError string
	}{{
		name:      "Non-struct top-level",
		cueExpr:   `"hello"`,
		wantError: `koala: top-level value must be a struct, got string`,
	}, {
		name: "Multiple top-level fields",
		cueExpr: `a: {}
b: {}`,
		wantError: `koala: top-level struct has multiple fields`,
	}, {
		name:      "Empty struct",
		cueExpr:   ``,
		wantError: `koala: top-level struct has no fields`,
	}}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			ctx := cuecontext.New()
			v := ctx.CompileString(test.cueExpr)
			qt.Assert(t, qt.IsNil(v.Err()))

			var buf bytes.Buffer
			enc := koala.NewEncoder(&buf)
			err := enc.Encode(v)
			qt.Assert(t, qt.IsNotNil(err))
			qt.Assert(t, qt.StringContains(err.Error(), test.wantError))
		})
	}
}

func TestRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		inputXML string
		wantXML  string
	}{{
		name: "Simple elements",
		inputXML: `<note>
	<from>Jani</from>
	<heading>Reminder</heading>
	<body>Don't forget me this weekend!</body>
</note>`,
		wantXML: `<?xml version="1.0" encoding="UTF-8"?>
<note>
	<from>Jani</from>
	<heading>Reminder</heading>
	<body>Don't forget me this weekend!</body>
</note>
`,
	}, {
		name:     "Self-closing element",
		inputXML: `<root><item/></root>`,
		wantXML: `<?xml version="1.0" encoding="UTF-8"?>
<root>
	<item/>
</root>
`,
	}, {
		name:     "Attribute with text",
		inputXML: `<note alpha="abcd">hello</note>`,
		wantXML: `<?xml version="1.0" encoding="UTF-8"?>
<note alpha="abcd">hello</note>
`,
	}, {
		name: "Nested with attributes",
		inputXML: `<root>
	<message>Hello World!</message>
	<nested>
		<a1>one level</a1>
		<a2>
			<b>two levels</b>
		</a2>
	</nested>
</root>`,
		wantXML: `<?xml version="1.0" encoding="UTF-8"?>
<root>
	<message>Hello World!</message>
	<nested>
		<a1>one level</a1>
		<a2>
			<b>two levels</b>
		</a2>
	</nested>
</root>
`,
	}, {
		name: "Collections",
		inputXML: `<notes>
	<note alpha="abcd">hello</note>
	<note alpha="abcdef">goodbye</note>
</notes>`,
		wantXML: `<?xml version="1.0" encoding="UTF-8"?>
<notes>
	<note alpha="abcd">hello</note>
	<note alpha="abcdef">goodbye</note>
</notes>
`,
	}}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			// Decode XML to CUE AST.
			dec := koala.NewDecoder("input.xml", strings.NewReader(test.inputXML))
			cueExpr, err := dec.Decode()
			qt.Assert(t, qt.IsNil(err))

			// Build cue.Value from the AST.
			rootCueFile, err := astutil.ToFile(cueExpr)
			qt.Assert(t, qt.IsNil(err))

			ctx := cuecontext.New()
			v := ctx.BuildFile(rootCueFile)
			qt.Assert(t, qt.IsNil(v.Err()))

			// Encode back to XML.
			var buf bytes.Buffer
			enc := koala.NewEncoder(&buf)
			err = enc.Encode(v)
			qt.Assert(t, qt.IsNil(err))
			qt.Assert(t, qt.Equals(buf.String(), test.wantXML))
		})
	}
}
