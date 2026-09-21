package epay

import "testing"

func TestYuanToFen(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"0.01", 1, false},
		{"10.00", 1000, false},
		{"10", 1000, false},
		{"1.2", 120, false},
		{"0", 0, true},
		{"-1", 0, true},
		{"0.001", 0, true},
		{"abc", 0, true},
		{"", 0, true},
	}
	for _, tc := range cases {
		got, err := YuanToFen(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%q: 期望错误", tc.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("%q: got %d want %d", tc.in, got, tc.want)
		}
	}
}
