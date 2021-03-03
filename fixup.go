package main

import (
	"context"
	"fmt"

	"github.com/rivo/tview"
)

func fixup(_ context.Context, st store, _ []string) error {
	reqs, err := st.requests()
	if err != nil {
		return err
	}

	app := tview.NewApplication()

	list := tview.NewList().ShowSecondaryText(false)
	list.SetBorder(true).SetTitle("requests")

	startText := tview.NewTextView()
	startText.SetDynamicColors(true)

	start := tview.NewFlex()
	start.AddItem(startText, 0, 1, false)

	endText := tview.NewTextView()
	endText.SetDynamicColors(true)

	end := tview.NewFlex()
	end.AddItem(endText, 0, 1, false)

	infoText := tview.NewTextView()
	infoText.SetDynamicColors(true)

	info := tview.NewFlex()
	info.AddItem(infoText, 0, 1, false)

	bottom := tview.NewFlex()
	bottom.AddItem(start, 0, 1, false)
	bottom.AddItem(end, 0, 1, false)
	bottom.AddItem(info, 0, 1, false)

	flex := tview.NewFlex().
		AddItem(list, 0, 1, true).
		AddItem(bottom, 0, 3, false)
	flex.SetDirection(tview.FlexRow)

	rrs := make([]requestRenderer, 0, len(reqs))
	for _, req := range reqs {
		rr := requestRenderer{
			req:       req,
			handler:   newDefaultRequestHandler(st, req),
			startText: startText,
			endText:   endText,
			infoText:  infoText,
		}

		list.AddItem(rr.req.String(), "", 0, rr.selected)

		rrs = append(rrs, rr)
	}

	list.SetChangedFunc(func(index int, mainText string, secondaryText string, shortcut rune) {
		rrs[index].changed()
	})

	rrs[0].changed()

	return app.SetRoot(flex, true).Run()
}

type requestRenderer struct {
	req     request
	handler requestHandler

	startText *tview.TextView
	endText   *tview.TextView
	infoText  *tview.TextView
}

func (r requestRenderer) selected() {
	r.changed()
}

func (r requestRenderer) changed() {
	r.startText.Clear()
	r.endText.Clear()
	r.infoText.Clear()

	attempt := r.handler.handleAttempt()

	if attempt.startErr != nil {
		fmt.Fprintln(r.startText, "[red]Error:", attempt.startErr)
		return
	}

	for _, seg := range attempt.startSegments {
		fmt.Fprintln(r.startText, seg)
	}

	if attempt.endErr != nil {
		fmt.Fprintln(r.endText, "[red]Error:", attempt.endErr, "[white]")
		return
	}

	for _, seg := range attempt.endSegments {
		fmt.Fprintln(r.endText, seg)
	}

	if attempt.routeErr != nil {
		fmt.Fprintln(r.infoText, "[red]Error:", attempt.routeErr)
		return
	}

	for _, seg := range attempt.routeSegments {
		fmt.Fprintln(r.infoText, seg)
	}
}
