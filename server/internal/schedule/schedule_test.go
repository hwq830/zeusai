package schedule

import (
	"testing"
	"time"
)

func TestNext(t *testing.T) {
	sh, _ := time.LoadLocation("Asia/Shanghai")
	// Friday 2026-10-23 09:00 Shanghai.
	now := time.Date(2026, 10, 23, 9, 0, 0, 0, sh)
	cases := []struct {
		name string
		s    Schedule
		want time.Time
	}{
		{"daily later today", Schedule{Freq: "daily", Time: "20:30", TZ: "Asia/Shanghai"}, time.Date(2026, 10, 23, 20, 30, 0, 0, sh)},
		{"daily tomorrow", Schedule{Freq: "daily", Time: "08:00", TZ: "Asia/Shanghai"}, time.Date(2026, 10, 24, 8, 0, 0, 0, sh)},
		{"workdays skip weekend", Schedule{Freq: "daily", Weekdays: []int{1, 2, 3, 4, 5}, Time: "08:00", TZ: "Asia/Shanghai"}, time.Date(2026, 10, 26, 8, 0, 0, 0, sh)},
		{"weekly sunday", Schedule{Freq: "weekly", Weekdays: []int{7}, Time: "10:00", TZ: "Asia/Shanghai"}, time.Date(2026, 10, 25, 10, 0, 0, 0, sh)},
		{"monthly", Schedule{Freq: "monthly", MonthDay: 1, Time: "08:00", TZ: "Asia/Shanghai"}, time.Date(2026, 11, 1, 8, 0, 0, 0, sh)},
	}
	for _, c := range cases {
		if err := c.s.Validate(); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := c.s.Next(now); !got.Equal(c.want) {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestValidate(t *testing.T) {
	bad := []Schedule{
		{Freq: "daily", Time: "8:00", TZ: "Asia/Shanghai"},
		{Freq: "daily", Time: "24:00", TZ: "Asia/Shanghai"},
		{Freq: "weekly", Time: "08:00", TZ: "Asia/Shanghai"},
		{Freq: "monthly", MonthDay: 31, Time: "08:00", TZ: "Asia/Shanghai"},
		{Freq: "hourly", Time: "08:00", TZ: "Asia/Shanghai"},
		{Freq: "daily", Time: "08:00", TZ: "Mars/Base"},
	}
	for _, s := range bad {
		if s.Validate() == nil {
			t.Errorf("expected invalid: %+v", s)
		}
	}
}
