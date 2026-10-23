// Package schedule models the contract's Schedule and computes delivery times.
package schedule

import (
	"fmt"
	"slices"
	"time"

	"github.com/hwq830/zeusai/server/internal/apperr"
)

type Schedule struct {
	Freq     string `json:"freq"` // daily | weekly | monthly
	Weekdays []int  `json:"weekdays,omitempty"`
	MonthDay int    `json:"monthDay,omitempty"`
	Time     string `json:"time"` // HH:MM in TZ
	TZ       string `json:"tz"`
}

// Clock returns the delivery hour and minute.
func (s Schedule) Clock() (int, int) {
	var h, m int
	_, _ = fmt.Sscanf(s.Time, "%d:%d", &h, &m)
	return h, m
}

func (s Schedule) Location() *time.Location {
	loc, err := time.LoadLocation(s.TZ)
	if err != nil {
		return time.UTC
	}
	return loc
}

func (s Schedule) Validate() error {
	if _, err := time.LoadLocation(s.TZ); err != nil || s.TZ == "" {
		return apperr.Invalid("时区无效")
	}
	var h, m int
	if n, _ := fmt.Sscanf(s.Time, "%d:%d", &h, &m); n != 2 || h < 0 || h > 23 || m < 0 || m > 59 || len(s.Time) != 5 {
		return apperr.Invalid("时间格式应为 HH:MM")
	}
	for _, d := range s.Weekdays {
		if d < 1 || d > 7 {
			return apperr.Invalid("weekdays 取值为 1–7")
		}
	}
	switch s.Freq {
	case "daily":
	case "weekly":
		if len(s.Weekdays) == 0 {
			return apperr.Invalid("每周简报需要选择星期")
		}
	case "monthly":
		if s.MonthDay < 1 || s.MonthDay > 28 {
			return apperr.Invalid("每月简报的日期为 1–28")
		}
	default:
		return apperr.Invalid("freq 应为 daily / weekly / monthly")
	}
	return nil
}

// isoWeekday maps time.Weekday to 1=Mon … 7=Sun.
func isoWeekday(d time.Weekday) int {
	if d == time.Sunday {
		return 7
	}
	return int(d)
}

// Matches reports whether a delivery is due on the given local date.
func (s Schedule) Matches(day time.Time) bool {
	switch s.Freq {
	case "daily":
		return len(s.Weekdays) == 0 || slices.Contains(s.Weekdays, isoWeekday(day.Weekday()))
	case "weekly":
		return slices.Contains(s.Weekdays, isoWeekday(day.Weekday()))
	case "monthly":
		return day.Day() == s.MonthDay
	}
	return false
}

// Next returns the first delivery time strictly after t.
func (s Schedule) Next(t time.Time) time.Time {
	loc := s.Location()
	h, m := s.Clock()
	local := t.In(loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	for i := 0; i < 62; i++ {
		d := day.AddDate(0, 0, i)
		at := time.Date(d.Year(), d.Month(), d.Day(), h, m, 0, 0, loc)
		if at.After(t) && s.Matches(d) {
			return at
		}
	}
	return time.Time{}
}

// LocalDate returns t's calendar date in the schedule's zone, as midnight UTC (for date columns).
func LocalDate(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.UTC)
}
