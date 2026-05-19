package main

import "testing"

func TestCalculateSettlementRowsSplitsPointSettlementByPoints(t *testing.T) {
	rows, err := calculateSettlementRows(settlementTypePoints, 40000, 50, []settlementRow{
		{PlayerID: 1, Points: 6},
		{PlayerID: 2, Points: 4},
		{PlayerID: 3, Points: -8},
		{PlayerID: 4, Points: -2},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := amountsByPlayer(rows)
	want := map[int]int{1: 24000, 2: 16000, 3: -32000, 4: -8000}
	if !equalAmounts(got, want) {
		t.Fatalf("amounts=%v want %v", got, want)
	}
}

func TestCalculateSettlementRowsSplitsCommonDebtBetweenEqualAndGapShares(t *testing.T) {
	rows, err := calculateSettlementRows(settlementTypeCommonDebt, 40000, 50, []settlementRow{
		{PlayerID: 1, Points: 5},
		{PlayerID: 2, Points: 0},
		{PlayerID: 3, Points: -3},
		{PlayerID: 4, Points: -1},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := amountsByPlayer(rows)
	// 200 kr. are shared equally; 200 kr. follow the distance from the winner.
	want := map[int]int{1: -5000, 2: -10263, 3: -13421, 4: -11316}
	if !equalAmounts(got, want) {
		t.Fatalf("amounts=%v want %v", got, want)
	}
}

func TestCalculateSettlementRowsRespectsConfiguredCommonDebtShare(t *testing.T) {
	rows, err := calculateSettlementRows(settlementTypeCommonDebt, 40000, 75, []settlementRow{
		{PlayerID: 1, Points: 10},
		{PlayerID: 2, Points: 4},
		{PlayerID: 3, Points: -4},
		{PlayerID: 4, Points: -10},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := amountsByPlayer(rows)
	want := map[int]int{1: -7500, 2: -9000, 3: -11000, 4: -12500}
	if !equalAmounts(got, want) {
		t.Fatalf("amounts=%v want %v", got, want)
	}
}

func TestCalculateSettlementRowsDistributesRemainderDeterministically(t *testing.T) {
	rows, err := calculateSettlementRows(settlementTypePoints, 101, 50, []settlementRow{
		{PlayerID: 8, Points: 1},
		{PlayerID: 3, Points: 1},
		{PlayerID: 9, Points: -1},
		{PlayerID: 4, Points: -1},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := amountsByPlayer(rows)
	// Equal fractional remainders go to the lowest player id on each side.
	want := map[int]int{8: 50, 3: 51, 9: -50, 4: -51}
	if !equalAmounts(got, want) {
		t.Fatalf("amounts=%v want %v", got, want)
	}
}

func TestCalculateSettlementRowsRejectsUnusablePoints(t *testing.T) {
	if _, err := calculateSettlementRows(settlementTypePoints, 100, 50, []settlementRow{
		{PlayerID: 1, Points: 3},
		{PlayerID: 2, Points: 0},
	}); err != errSettlementNeedsDebtors {
		t.Fatalf("point settlement err=%v want %v", err, errSettlementNeedsDebtors)
	}
	if _, err := calculateSettlementRows(settlementTypePoints, 100, 50, []settlementRow{
		{PlayerID: 1, Points: -3},
		{PlayerID: 2, Points: 0},
	}); err != errSettlementNeedsSides {
		t.Fatalf("point settlement err=%v want %v", err, errSettlementNeedsSides)
	}
	rows, err := calculateSettlementRows(settlementTypeCommonDebt, 100, 50, []settlementRow{
		{PlayerID: 1, Points: 3},
	})
	if err != nil {
		t.Fatalf("common debt err=%v", err)
	}
	if got, want := rows[0].AmountCents, -100; got != want {
		t.Fatalf("single-player common debt=%d want %d", got, want)
	}
}

func amountsByPlayer(rows []settlementRow) map[int]int {
	out := make(map[int]int, len(rows))
	for _, row := range rows {
		out[row.PlayerID] = row.AmountCents
	}
	return out
}

func equalAmounts(a, b map[int]int) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}
