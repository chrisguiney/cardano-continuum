package market

// ProfilePrefix starts the profile name of every exchange's actor.
const ProfilePrefix = "exchange-"

// ProfileName is the profile of the actor that runs the named exchange:
// its hot wallet, which the genesis or the faucet funds like any wallet.
func ProfileName(exchange string) string { return ProfilePrefix + exchange }
