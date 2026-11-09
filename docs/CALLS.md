# Voice calls

Fridolin can take and make WhatsApp voice calls, with `calls.enabled` in
config.json ([CONFIGURATION.md](CONFIGURATION.md#calls)):

- **You call Fridolin's number** and talk to the brain that gets your
  direct messages, as you would write to it.
- **An errand calls someone** instead of writing to them
  ([BRAINS.md](BRAINS.md#errands)): `contact_person` with `call: true`.

Calls are turn by turn, like a walkie-talkie with manners. Fridolin
listens until you pause, then what you said is transcribed, answered, and
read aloud. It does not listen while it speaks, so wait for it to finish.

## Setting it up

1. **ElevenLabs** (Integrations → ElevenLabs): a key, and a voice in
   `connections.elevenlabs.voice` (Integrations → ElevenLabs → voice). Fridolin
   speaks with ElevenLabs' fast multilingual model (`eleven_flash_v2_5`).
2. **Transcription**: through the OpenAI connection when it has a key
   (`gpt-4o-mini-transcribe`), else through ElevenLabs (`scribe_v1`).
3. `"calls": {"enabled": true}` in config.json, and a restart: the call
   stack goes onto the WhatsApp connection before it connects.
4. Optional: `calls.greeting` and `calls.notice` in your language.

What a call costs is counted against the brain's monthly limits of the
two connections (`brain_monthly_usd`), as its tools' calls are.

## Calling Fridolin

- Only a number in `allowed_numbers` is answered. A call from anyone
  else is not touched: it rings on the phone that holds Fridolin's
  account, as before.
- Fridolin answers with `calls.greeting`. Each thing you say goes to the
  brain that gets your direct messages, as your own words (a run that
  answers you, with what that allows: your GitHub grant, errands without
  asking for someone written to before). The run is told it is on a call
  and answers in short spoken sentences. Fridolin says "One moment."
  while the brain works, then reads the answer.
- The answer is not sent to the chat as well.
- A call ends when you hang up, after two minutes of silence, or after
  15 minutes.

## Errand calls

`contact_person(..., call: true)`:

- Only someone Fridolin wrote to before: the first contact is always a
  message, which they can read and place.
- The same approvals as a message: the owner's yes in a poll, unless you
  asked for it in your own direct chat. The poll shows what Fridolin will
  say first.
- At most five errand calls a day.
- Fridolin opens with the errand's introduction, `calls.notice` (that it
  is an AI and keeps notes), and the errand's first message. What they
  say goes to the errand's run, which is told it is on a call; its
  answers are read aloud and its rules are the errand's rules: a question
  for you ends the call ("I'll get back to you on WhatsApp"), done ends it
  and the errand, and abort says the bridge's own words: a warning the
  first time, and the call goes on; the end and a block after that, or
  for a serious try ([BRAINS.md](BRAINS.md#errands)).
- On a call too, the errand may offer your free times and book one, as
  far as it may by message ([BRAINS.md](BRAINS.md#calendar-in-errands));
  with `book_after_yes`, the confirmation follows by message.
- Nobody picks up within 45 seconds: the errand ends and you are told.
- A call that ends before the errand did (they hang up, 45 seconds of
  silence, 10 minutes) leaves the errand open: it goes on by message.
- The conversation, what they said as transcribed, is the errand's, in
  bridge.db, as for messages.

## Limits and caveats

- **The call stack is not WhatsApp's own.** It is
  [meowcaller](https://github.com/purpshell/meowcaller), an open
  reimplementation of WhatsApp's call protocol, pinned to its last
  version on upstream whatsmeow (Renovate leaves it alone). WhatsApp may
  change the protocol, and a number that places calls by software may
  look like spam to WhatsApp: a ban would take all of Fridolin offline.
  Hence calls only to people Fridolin wrote to, and few of them.
- Some phones may not connect: meowcaller speaks WhatsApp's MLow codec,
  and its Opus fallback is not finished.
- The voice detector is simple: a loud line or a long pause in the middle
  of a sentence can cut a turn. Speak, then pause.
- In many places a call may not be recorded without consent. Fridolin
  keeps no recording, only the transcript, and says at the start of an
  errand call that it is an AI and keeps notes.

## The audit log

Your calls to Fridolin are `voice_call` lines (answered, ended, with the
number of turns); an errand's calls are its `errand` lines (opened,
called, said, answered, ended). Neither holds what was said.
