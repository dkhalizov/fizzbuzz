// Package fizzbuzz streams the generalized FizzBuzz sequence as a JSON array
// of strings, byte-identical to json.Marshal of the equivalent []string.
//
// # Segment loop
//
// The generator never divides. It tracks the next multiple of each divisor,
// next1 and next2. Everything before min(next1, next2) is a plain number, so
// the generator copies those runs without a test. The replacement logic runs
// only at the multiples. For int1=3, int2=5 (F = str1, B = str2):
//
//	i         1   2   3   4   5   6   7   8   9  10  11  12  13  14  15
//	out       1   2   F   4   B   F   7   8   F   B  11   F  13  14  FB
//	         └────┘      └┘          └────┘          └┘      └────┘  plain runs
//	                  ▲       ▲   ▲           ▲   ▲       ▲           ▲  events
//
// At an event, i == next1 emits str1 and advances next1 += int1. The same
// applies to str2. When both match, the output is str1+str2. Thus int1 == int2
// and pairs that are not coprime need no special case.
//
// # Decimal counter
//
// Plain numbers are not formatted. The counter keeps the complete element
// `,"129"` and increments its ASCII digits in place:
//
//	,"129"  ─inc─▶  ,"130"     9 → 0 carries left; one extra digit on 99 → 100
//
// An increment touches 10/9 digits on average, and emitting a number is one
// copy of that element.
//
// # Streaming and size
//
//	JSONSize: exact bytes in O(log limit), counted per decimal band
//	   │
//	   ├─▶ Validate rejects responses above MaxResponseBytes
//	   └─▶ Content-Length, known before generating
//
//	WriteJSON: fill pooled 32 KiB buffer ─▶ Write ─▶ repeat   (memory O(1) in limit)
//
// # Block templates
//
// With K = 10^k and L = lcm(int1, int2, K), a block of L elements after a
// multiple of L has fixed element types and fixed low k digits. Only the high
// digits change. The writer builds the block one time for each digit width,
// and for each later block it stores the high digits into the holes and
// writes the block buffer as it is:
//
//	,"7|9998" ,"7|9999" ,"8|0000" ,"8|0001"     k = 4, holes before |
//
// The first block, blocks that cross a power of ten and the tail use the
// element loop. block.go has the details.
//
// When int1 or int2 is 1 no numbers appear and the bytes repeat every
// max(int1, int2) elements. If one period fits the buffer, a chunk of whole
// periods is written repeatedly.
package fizzbuzz
