# Instinct layer: gestures before the reply

Mynah can react physically *before* the brain finishes composing its answer. A fast decision model watches the live transcript and picks one of the avatar's baked gesture clips — nod, wave, shake, laugh, shy, wink — or nothing. The model that talks does not move; the model that moves does not talk. The split comes from [open-annie](https://github.com/rehan-remade/open-annie) and runs on [Jev](https://typesafe.ai) (TypeSafe System One), with a local rules provider as the floor and the fallback.

## How it works

1. **Clips.** Every gesture is a short pre-baked clip in the same frame domain as the avatar's idle loop (see [Avatars](./avatars)). Bake them from the console's bake API with `kind: "action"` and a LivePortrait driving template:

   ```bash
   curl -sk -X POST https://<host>:9443/api/v1/avatars/bake \
     -H "authorization: Bearer $TOKEN" -H "content-type: application/json" \
     -d '{"avatar_id":"luna","kind":"action","action":"shake","motion_ref":"builtin:shake_face","apply":true}'
   ```

   Built-in templates that make good gestures: `shake_face`, `laugh`, `shy`, `wink`. Clips install hot and persist in `config.avatar.actions`.

2. **Questions.** While the visitor talks, the engine asks one closed question per ~450 ms of new text: *which of these clips fits right now, or none?* While the avatar speaks it also asks *does the reply text commit to a gesture?* ("let me wave" → yes). Each option carries a one-line description so the model knows what the clip looks like.

3. **Gate.** A clip plays only when the pick is confident (default 0.6; 0.8 while text is still streaming), only once per cooldown (4 s), and only when the avatar is not mid-reply (speech frames keep priority — the clip waits for the next ask).

4. **Fallback.** No key, timeout, 429, or three failures in a row → the rules provider answers the same questions from keywords. Nothing ever blocks a turn.

## Providers

| Provider | Latency | Cost | Needs |
|---|---|---|---|
| `rules` | 0 ms | free | nothing |
| `jev` | ~100–500 ms | $0.042 / M input tokens | TypeSafe API key; an egress proxy where `api.typesafe.ai` answers 451 |

Configure in **Console → Config → Instinct** (config group `instinct`): enable, provider, key, optional `proxy_url`, thresholds. New sessions pick up changes.

## Watching it work

The visitor page's DataChannel receives `{"type":"instinct", clip, reason, provider, latency_ms, answers}` for every decision; cored logs them as `[instinct <session>]`. The dsh plugin's `mynah_action` tool triggers the same clips by hand.
