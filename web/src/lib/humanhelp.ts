import type { FieldHelp, Term } from "./help";

/**
 * Definitions for the People page and the owner parts of the other pages.
 * Every number about people is synthetic: drawn from the profile's humans
 * section, not fitted from chain data.
 */
export const HUMAN_HELP: Record<string, FieldHelp> = {
  human: { label: "Human", help: "A simulated person. A human owns one or more wallets; the wallets are what the chain sees, the human is who decides. Its number is its own, separate from the wallets' actor numbers." },
  motivation: { label: "Motivation", help: "What the human wants: hodl (hold), yield (earn staking rewards), profit (trade), civic (vote), dapp (use apps) or opportunist. Every wallet the human owns scores its choices with this agenda." },
  purpose: { label: "Purpose", help: "What a wallet is for to its owner: savings (kept, receives purchases, never trades itself), spending (everyday payments and trades), staking, governance, trading, or general (no limit). A purpose rules out the intents the wallet is not used for." },
  wealth_tier: { label: "Tier", help: "The human's wealth tier: low, mid, high or whale. The tier sets its pay, bills, starting savings, how many wallets it keeps and how likely it is to lose or find a job." },
  wallets: { label: "Wallets", help: "How many wallets the human owns. Richer humans and power users tend to keep more, for example savings apart from spending, or stake split over several pools." },
  employed: { label: "Employed", help: "Whether the human had a job at its last payday. Out of work it receives only a benefit, a share of its pay, but its bills stay the same." },
  income: { label: "Monthly pay", help: "What the human earns in a month while employed, in its own currency. It is drawn once, in US dollars, and converted at the human's birth, so later exchange-rate moves change its ADA price but not its pay." },
  bills: { label: "Monthly bills", help: "The human's monthly obligations (rent, food, debts) as an amount: its pay times its bills ratio. They are paid on payday from fiat; fiat never goes below zero, and an unpaid rest forces a sale of ADA." },
  fiat: { label: "Fiat", help: "The money the human holds outside the chain, in its own currency. It goes up with pay and sales of ADA, and down with bills and purchases." },
  held: { label: "Held", help: "The part of the fiat set aside for buy orders that have not settled yet. An order holds what it costs when it is placed and spends it when its ADA is final; a cancelled order gives it back." },
  share: { label: "Invests", help: "The share of disposable income (pay less bills) the human puts into ADA. It buys in small amounts several times a month rather than all at once." },
  style: { label: "Style", help: "How the human times purchases. dca (dollar-cost averaging): the same amount every time. dip: more after ADA has fallen, less after it rose. momentum: the reverse." },
  buffer: { label: "Buffer", help: "Months of bills the human keeps in fiat before it invests anything. A purchase never spends into the buffer." },
  spread: { label: "Spread", help: "How strongly the human spreads its wallets' delegations over different stake pools: 0 puts them all on the best pool, 1 avoids any pool another of its wallets already uses." },
  invested: { label: "Bought from income", help: "The fiat the human's purchases from income spent, once their ADA settled. Buys a wallet's own agenda chose (a rally, a dip) are counted apart." },
  lovelace: { label: "ADA", help: "The ADA all the human's wallets hold, as the planner settled it at the last checkpoint." },
  transfer: { label: "Transfer", help: "A payment between two wallets of the same human: on chain it looks like any payment, but it is the person moving its own money, so it is not counted as a payment between people." },
};

/** HUMAN_GLOSSARY explains the human and wallet model in plain words. */
export const HUMAN_GLOSSARY: Term[] = [
  { term: "Human", def: HUMAN_HELP.human!.help },
  { term: "Wallet", def: "A set of keys and the coins they control: what the chain sees and the engine's actor. A person may hold many; the chain cannot tell which wallets belong to one person." },
  { term: "Owner", def: "The human a wallet belongs to. Wallets of one owner share its motivation, temperament and money, but each has its own keys, balance and delegation." },
  { term: "Purpose", def: HUMAN_HELP.purpose!.help },
  { term: "Motivation", def: HUMAN_HELP.motivation!.help },
  { term: "Tier", def: HUMAN_HELP.wealth_tier!.help },
  { term: "Fiat", def: HUMAN_HELP.fiat!.help },
  { term: "Disposable income", def: "Pay less bills in a month, never below zero: what a human can save or invest." },
  { term: "DCA", def: "Dollar-cost averaging: buying a fixed amount on a regular schedule, whatever the price, so the average cost evens out." },
  { term: "Forced sale", def: "A sale of ADA the human makes because a month's bills were more than its fiat. It sells from a spending or trading wallet, after moving ADA out of savings if it must." },
  { term: "Transfer", def: HUMAN_HELP.transfer!.help },
  { term: "Address clustering", def: "Guessing which addresses belong to one person from how they move coins together. Continuum's humans are the ground truth such guesses can be checked against." },
  { term: "Synthetic", def: "Made up to be plausible, not measured. Every number about people (pay, bills, jobs, wallets per person) is synthetic, set in the profile's humans section." },
];
