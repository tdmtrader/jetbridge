# Implementation Plan: Fly CLI Sidecar Event Support

## Phase 1: Sidecar Event Rendering

- [ ] Write tests for sidecar event rendering in `fly/eventstream/render_test.go`
- [ ] Add `case event.Sidecar` to `Render()` switch — parse plan, print header, register origin mapping
- [ ] Write tests for sidecar log prefixing
- [ ] Modify `case event.Log` to prefix output when origin matches a registered sidecar
- [ ] Phase 1 Manual Verification — run `fly watch` against `sidecar-ui-test/sidecar-demo`
