# AJB_MP_SIDECAR_PROXY

Standalone Go sidecar for connecting AJB PC Port UDP gameplay through EOS P2P.
It does not modify the game port.
Details TBA.

The EOS headers are supplied by the external EOS SDK, not this repository.

AI assistance has been used for debugging purposes and testing.

## Direct gameplay connection test

The AJB browser uses Unreal's LAN `FindSessions` path, while gameplay uses a
separate UDP connection. The checked-out AJB mod source includes the browser
call but not Unreal Engine's LAN discovery packet implementation, so the
sidecar cannot safely synthesize browser replies from this source alone.

For now, gameplay can be tested without the server browser.

1. On the host, start AJB as a listen server on UDP 1170, then run
   `./dist/sidecar host`.
2. Give the printed EOS lobby ID to the other player.
3. On the other player's computer, run `./dist/sidecar join LOBBY_ID` and keep
   it running.
4. In the joining game's Unreal console, enter `open 127.0.0.1:1170`.

The sidecar prints the direct-connect command after joining. This tests the
gameplay tunnel through EOS, not LAN server-browser discovery.
