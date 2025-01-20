# Conway

The makerspace management software used by TheLab.ms in Richardson, TX.


## What does it do?

Tons of stuff! Billing for monthly membership dues (with Stripe and kind of PayPal for legacy reasons), door access controls, etc.


## That's cool! Can I use it for my makerspace?

That should in theory be possible. Open a GH issue if you're serious and we'll write up some docs to help you through the setup process.


## Development

Just clone it down and `go run ./cmd/conway`! It will be listening on localhost port 8080.

### Deployment

See: https://github.com/TheLab-ms/infra

### Architecture

Conway is very simple: the main process (`conway`) runs in the cloud, and its "agent" (`glider`) runs in the makerspace.

The main conway process uses sqlite for persistence and is exposed to the internet by Cloudflare tunnels.
Glider is a local cache process for building access controls, buffers events to handle cases where the internet is out, and can check on the status of various tools.
