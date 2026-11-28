// Package people is the People page's read model: the humans who own the
// wallets, their household money and the fiat that flows into and out of
// ADA. A Fold reads the log's human records in order (HumanBorn, HumanLeft,
// Income, Expense, Employment, Invested and the Traded records that name a
// human); Build joins it with each wallet's identity row, which carries its
// owner as of the last checkpoint (store.Owner). It holds no lock: the
// caller serialises Add and the reads.
package people
