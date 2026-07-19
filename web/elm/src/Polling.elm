module Polling exposing (Poll, handleDelivery, subscriptions)

{-| Periodic self-heal refetch for pages that keep themselves current by
polling the API on a clock.

A page declares its polling as data — one `Poll` per cadence, pairing a clock
interval with a pure fetch-effects function — and derives both its
`subscriptions` and its clock-tick `handleDelivery` from that single list, so
the two can't drift apart.

The point of the indirection is an invariant that used to live in per-page
comments and was broken once (a 5s refetch clobbered an open edit form): a
poll tick only _fetches_. `handleDelivery` returns the model unchanged — a
poll's `fetch` may read the model (e.g. to stop polling a settled ticket) but
can only produce effects, so a page built on this helper cannot touch form
state from a tick. What a fetch's _callback_ may then overwrite is the
callback's own contract: only replace fetched data, never form state.

A page that additionally needs a model update on a tick (e.g. advancing a
"now" clock for elapsed-time labels) keeps that in its own `handleDelivery`
and composes it with this one, so the model write stays visible in the page
rather than smuggled into the polling.

-}

import EffectTransformer exposing (ET)
import Message.Effects exposing (Effect)
import Message.Subscription exposing (Delivery(..), Interval, Subscription(..))


{-| One polling cadence: every `interval`, emit `fetch model`. `fetch` is
pure; return `[]` to skip a beat (e.g. a terminal ticket that can no longer
change server-side).
-}
type alias Poll model =
    { interval : Interval
    , fetch : model -> List Effect
    }


subscriptions : List (Poll model) -> List Subscription
subscriptions polls =
    List.map (.interval >> OnClockTick) polls


handleDelivery : List (Poll model) -> Delivery -> ET model
handleDelivery polls delivery ( model, effects ) =
    case delivery of
        ClockTicked interval _ ->
            ( model
            , effects
                ++ (polls
                        |> List.filter (\poll -> poll.interval == interval)
                        |> List.concatMap (\poll -> poll.fetch model)
                   )
            )

        _ ->
            ( model, effects )
