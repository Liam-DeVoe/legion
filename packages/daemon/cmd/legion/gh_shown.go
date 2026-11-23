package main

// ghShown is place, the words that name value, a value a person gave legion gh (an argument, an
// option's value, a selector, an environment value, a search query), by its kind and where it
// stands ("a -R value", "a git flag after --", "an option the command's help does not list"). A
// refusal of legion gh prints no such value in any form: any of it can be a credential a person
// put there. value is passed so that each refusal path says which value its words stand for.
//
// No refusal formats such a value itself. Three other prints stand beside ghShown, each of
// something read from a value and checked first: ghWalk.named quotes a subcommand only where gh's
// own help lists it; ghNamed.shown prints the owner and the name a selector named, once
// checkedOwner and repositoryTarget admitted them; and remoteShown prints a remote's URL through
// the URL rule. TestNoRefusalPathPrintsACanary finds every call of ghShown in the source, keyed by
// the function that makes it and the source of its place argument (a literal, or a variable such
// as repositoryTarget's kind), and drives each with a canary in every slot.
// TestNoRefusalFormatsAPersonsValueItself holds every other place that formats a value to a
// reviewed table.
func ghShown(value, place string) string {
	return place
}
