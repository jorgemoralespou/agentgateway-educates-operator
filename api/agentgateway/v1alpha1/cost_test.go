package v1alpha1

import "testing"

// The conversion is the one place an authored dollar string becomes the integer
// the protocol carries. A float anywhere in this path would admit
// representation errors into a value compared for equality, so the parsing is
// done by hand and pinned here.
func TestParseDollarsToMicroDollars(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    int64
		wantErr bool
	}{
		{
			name: "an ordinary value",
			in:   "0.50",
			want: 500_000,
		},
		{
			name: "a whole dollar",
			in:   "1",
			want: 1_000_000,
		},
		{
			name: "several dollars",
			in:   "12.34",
			want: 12_340_000,
		},
		{
			// Whole cents would round this to zero and enforce nothing, which
			// is why the enforced unit is micro-dollars.
			name: "a sub-cent value survives",
			in:   "0.0001",
			want: 100,
		},
		{
			name: "the smallest representable value",
			in:   "0.000001",
			want: 1,
		},
		{
			name: "trailing zeros do not change the value",
			in:   "0.500000",
			want: 500_000,
		},
		{
			name: "trailing zeros beyond grain do not change the value",
			in:   "0.5000000000",
			want: 500_000,
		},
		{
			// Truncated, not rounded: truncating never enforces a budget
			// larger than the author asked for.
			name: "excess decimal places are truncated",
			in:   "0.1234567890",
			want: 123_456,
		},
		{
			name: "surrounding whitespace is tolerated",
			in:   "  2.50  ",
			want: 2_500_000,
		},
		{
			name:    "empty input is rejected",
			in:      "",
			wantErr: true,
		},
		{
			name:    "whitespace only is rejected",
			in:      "   ",
			wantErr: true,
		},
		{
			name:    "a negative value is rejected",
			in:      "-1.00",
			wantErr: true,
		},
		{
			name:    "an explicit plus sign is rejected",
			in:      "+1.00",
			wantErr: true,
		},
		{
			name:    "malformed input is rejected",
			in:      "abc",
			wantErr: true,
		},
		{
			name:    "a currency symbol is rejected",
			in:      "$1.00",
			wantErr: true,
		},
		{
			name:    "two decimal points are rejected",
			in:      "1.2.3",
			wantErr: true,
		},
		{
			name:    "a missing whole part is rejected",
			in:      ".5",
			wantErr: true,
		},
		{
			name:    "a missing fractional part is rejected",
			in:      "5.",
			wantErr: true,
		},
		{
			// Below micro-dollar grain there is nothing left to enforce, and a
			// budget of zero would reject every request.
			name:    "a value that truncates to zero is rejected",
			in:      "0.0000001",
			wantErr: true,
		},
		{
			name:    "an explicit zero is rejected",
			in:      "0",
			wantErr: true,
		},
		{
			name:    "an underscore separator is rejected",
			in:      "1_000",
			wantErr: true,
		},
		{
			// The overflow guard has to be checked before the multiply. A guard
			// comparing the product against the running total catches only the
			// cases that happen to wrap below it, so this value parsed cleanly
			// to an unrelated, smaller number.
			name:    "a value that overflows int64 micro-dollars is rejected",
			in:      "43896277737238.125098",
			wantErr: true,
		},
		{
			name:    "a value far beyond int64 is rejected",
			in:      "99999999999999999999.99",
			wantErr: true,
		},
		{
			name: "the largest value that still fits is accepted",
			in:   "9223372036854.775807",
			want: 9223372036854775807,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseDollarsToMicroDollars(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseDollarsToMicroDollars(%q) = %d, want an error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseDollarsToMicroDollars(%q) error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ParseDollarsToMicroDollars(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

// The cost chain mirrors the token one, except that no cost ceiling at all is
// the right default: this project maintains no pricing data and cannot invent a
// figure for someone else's provider account.
func TestResolveCostBudget(t *testing.T) {
	tests := []struct {
		name           string
		grant          string
		catalogDefault string
		catalogMax     string
		wantConfigured bool
		wantMicros     int64
		wantClamped    bool
		wantRequested  int64
		wantInherited  bool
	}{
		{
			name:           "nothing configured anywhere means no cost ceiling",
			wantConfigured: false,
		},
		{
			name:           "a grant's own value is used",
			grant:          "0.50",
			wantConfigured: true,
			wantMicros:     500_000,
		},
		{
			name:           "an unset grant takes the catalog default",
			catalogDefault: "0.25",
			wantConfigured: true,
			wantMicros:     250_000,
			wantInherited:  true,
		},
		{
			name:           "a grant's value wins over the catalog default",
			grant:          "1.00",
			catalogDefault: "0.25",
			wantConfigured: true,
			wantMicros:     1_000_000,
		},
		{
			name:           "a grant above the maximum is clamped",
			grant:          "5.00",
			catalogMax:     "1.00",
			wantConfigured: true,
			wantMicros:     1_000_000,
			wantClamped:    true,
			wantRequested:  5_000_000,
		},
		{
			name:           "a grant at the maximum is left alone",
			grant:          "1.00",
			catalogMax:     "1.00",
			wantConfigured: true,
			wantMicros:     1_000_000,
		},
		{
			name:           "the catalog default is itself bounded by the maximum",
			catalogDefault: "5.00",
			catalogMax:     "1.00",
			wantConfigured: true,
			wantMicros:     1_000_000,
			wantInherited:  true,
			// The operator's own two settings disagreeing, not the author's
			// doing.
			wantClamped: false,
		},
		{
			name:           "a maximum alone configures no ceiling",
			catalogMax:     "1.00",
			wantConfigured: false,
		},
		{
			// Validation rejects a malformed string at the API server. Reaching
			// here, falling back beats failing a running attendee's request.
			name:           "a malformed grant value falls back to the catalog default",
			grant:          "not-a-number",
			catalogDefault: "0.25",
			wantConfigured: true,
			wantMicros:     250_000,
			wantInherited:  true,
		},
		{
			name:           "a malformed value everywhere means no cost ceiling",
			grant:          "abc",
			catalogDefault: "def",
			wantConfigured: false,
		},
		{
			// The regression that matters most: treating an unreadable maximum
			// as absent silently removed the trust boundary, leaving an
			// operator believing they had a ceiling while grants spent without
			// one. It now fails visibly instead.
			name:           "an unreadable maximum clamps hard rather than disappearing",
			grant:          "1000",
			catalogMax:     "5.",
			wantConfigured: true,
			wantMicros:     1,
			wantClamped:    true,
			wantRequested:  1_000_000_000,
		},
		{
			name:           "a maximum of zero clamps hard rather than disappearing",
			grant:          "1000",
			catalogMax:     "0",
			wantConfigured: true,
			wantMicros:     1,
			wantClamped:    true,
			wantRequested:  1_000_000_000,
		},
		{
			// An absent maximum is different from an unreadable one, and must
			// still clamp nothing, or the ceiling would stop being opt-in.
			name:           "whitespace is treated as an absent maximum, not an unreadable one",
			grant:          "1000",
			catalogMax:     "   ",
			wantConfigured: true,
			wantMicros:     1_000_000_000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveCostBudget(tt.grant, tt.catalogDefault, tt.catalogMax)
			if got.Configured != tt.wantConfigured {
				t.Fatalf("Configured = %v, want %v", got.Configured, tt.wantConfigured)
			}
			if got.MicroDollars != tt.wantMicros {
				t.Errorf("MicroDollars = %d, want %d", got.MicroDollars, tt.wantMicros)
			}
			if got.Clamped != tt.wantClamped {
				t.Errorf("Clamped = %v, want %v", got.Clamped, tt.wantClamped)
			}
			if got.Inherited != tt.wantInherited {
				t.Errorf("Inherited = %v, want %v", got.Inherited, tt.wantInherited)
			}
			if tt.wantClamped && got.Requested != tt.wantRequested {
				t.Errorf("Requested = %d, want %d", got.Requested, tt.wantRequested)
			}
		})
	}
}
