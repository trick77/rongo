# Diagram text width: the advance budget for IBM Plex

rongo's own diagrams (`ui/src/diagramLayout.ts`) size every box from its label before anything is drawn, with a per-character budget rather than a measurement on the page (`textWidth` in `ui/src/diagramTheme.ts`). The budget has to hold under jsdom, where text has no width, and before the web font has loaded, so a layout is a function of its text alone.

## What was measured

Chrome, the self-hosted faces from `ui/src/fonts` loaded (`document.fonts.check` true), `CanvasRenderingContext2D.measureText` over corpus labels, width divided by size and by character count:

| label | Sans 400, 13px | Sans 600, 13.5px | Mono 400, 12px |
|---|---|---|---|
| opencode run, at most 40 steps | 0.473 | 0.485 | 0.600 |
| Comment: verify error + summary | 0.488 | 0.504 | 0.600 |
| Bestellung aufgeben | 0.485 | 0.501 | 0.600 |
| Fehlermeldung anzeigen | 0.500 | 0.518 | 0.600 |
| Schwellenwert überschritten? | 0.479 | 0.498 | 0.600 |
| Log error and return shipment (not queued) | 0.462 | 0.477 | 0.600 |
| Receive shipment request | 0.485 | 0.501 | 0.600 |
| WWWWMMMM mmmm (worst case) | 0.812 | 0.835 | 0.600 |
| iiii llll (best case) | 0.257 | 0.277 | 0.600 |

## Budget

Mono 0.60, exact. Sans 0.56: above every real label at either weight (0.46 to 0.52), so a box is never short for its label. A label of capitals and wide letters can still run past it; the 16px padding each side is the margin for that.

The first budget was 0.60 for both faces. At that value every box on the main path wrapped a line early: "opencode run, at most 40 steps" broke onto two lines in a box it fits on one.
