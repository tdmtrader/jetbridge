module Views.Truncate exposing (middle)

{-| Middle-truncation for short labels.

CSS `text-overflow: ellipsis` only truncates the END of a string, which throws
away the very suffix that distinguishes two otherwise-identical labels — e.g.
two ticket chips that share a long prefix and differ only in a trailing
"(T9 only)". `middle` instead elides the centre with a single "…", keeping both
the head and the distinguishing tail visible.
-}


{-| Truncate `text` to at most `maxLen` characters, dropping the middle and
inserting a "…" so the head and tail both survive. Strings already within
budget are returned unchanged. A budget of 1 or less can't fit head + ellipsis
+ tail, so the original is returned as-is (the caller's `overflow: hidden` is
the fallback there).
-}
middle : Int -> String -> String
middle maxLen text =
    if maxLen <= 1 || String.length text <= maxLen then
        text

    else
        let
            -- One character is spent on the ellipsis; split the rest with the
            -- extra char (if any) going to the head so the prefix stays legible.
            budget =
                maxLen - 1

            headLen =
                (budget + 1) // 2

            tailLen =
                budget - headLen
        in
        String.left headLen text ++ "…" ++ String.right tailLen text
