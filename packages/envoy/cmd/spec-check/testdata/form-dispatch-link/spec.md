:::requirement{#a-claim-survives-a-restart kind="behavior"}
Legion keeps the claim of an issue after a restart, as dispatch://ACME-12 says.
:::

:::check{#a-lost-claim-is-reported serves="a-claim-survives-a-restart" subject="production" procedure="agent"}
Claim: The daemon reports a claim that it cannot read after a restart.
Procedure: An agent stops the database during a restart and reads the log of the daemon.
It must find one report for the claim.
Failing example: A daemon that starts with no claims and writes no report.
:::
