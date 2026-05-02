# Conway

The makerspace management software used by TheLab.ms in Richardson, TX.


## What does it do?

Tons of stuff! Billing for monthly membership dues (with Stripe and kind of PayPal for legacy reasons), door access controls, etc.


## That's cool! Can I use it for my makerspace?

That should in theory be possible. Open a GH issue if you're serious and we'll write up some docs to help you through the setup process.


## Development

Install Go 1.24(ish), then just `make dev` and browse to http://localhost:8080.
The login flow will print the 5-digit code and login link to the console instead of actually sending an email.

Run `make seed` to insert a leadership account for `dev@localhost`.

### Deployment

See: https://github.com/TheLab-ms/infra

