package specgraph

import (
	"crypto/sha256"
	"fmt"
	"maps"
	"slices"
	"testing"
	"testing/fstest"
)

// sweepFloors is, for each sweep and each base (sweepBases), the count of inputs that sweepAgainst
// judged when the table was written, by what the reader did with each: read it, as the base or as
// another state, or refused it, and the digest of the base's files and of each judged input's files
// and kind, in the order of the inputs (sweepFloor, sweepCount.judged). The sweeps are the tree
// sweep (TestNoInputThatSaysNothingMovesAStateTowardTrue), the members added
// (TestAddingAMemberMovesNoStateTowardTrue), the texts inside a block and the attributes left out
// (TestNoNameOfNothingInsideABlockMovesAStateTowardTrue), and the refusals added
// (TestAddingARefusalOfCIMovesAStateTowardTrueOnlyByTakingOutItsCommitsResults). A sweep that judges
// fewer inputs of a base of either kind fails by name (atFloor), and so does a sweep whose inputs of
// a base hold, in their order, other bytes or read as other kinds than the digest's, and a base
// with no floor or a floor of no base (floorsCover). So a fault that drops inputs, of a sweep's
// meaning or of its writers, a sweep that builds inputs and does not derive them or drops an input
// from its judgment, a fault that moves inputs from one kind to the other, as many each way as it
// likes, and a change of the form of an input that keeps its kind (two spaces for one) cannot pass.
// The floors count what the reader did with an input, not what the judgment found: a judgment that
// stops reporting an input that moves a state toward true, and counts it as read, meets them. That
// judgment fails by its known positive (knownPositive), which each sweep judges at silent-leg and
// must report, and which no floor counts. A base, a scenario, a sweep or a reader that adds, takes
// out, moves or writes otherwise an input changes the digest, so the table is written again, on
// purpose, from a run of the sweeps. It is a function that gives a new table at each call, so that
// no writer can lower a floor (sweepMaps).
func sweepFloors() map[string]map[string]sweepFloor {
	return map[string]map[string]sweepFloor{
		"tree": {
			"carry": {read: 92, refused: 887, digest: "8612610708591fd45cd19a3cd1bc520e2203c8b494ae2f4ddc7f7a8fed8aadb3"},
			"carry with a failure of c0, c1's parent, and a later pass of c2":            {read: 120, refused: 1114, digest: "953854c3e818e451c1ecb086bc681053817c5863885b7b6f9b71e1d3ee93e9d5"},
			"carry with a later failing example planted by no step":                      {read: 104, refused: 934, digest: "5af647eac3450c635d295b5fb1641d372324dc0313f76cf8f2c5e0b1f630376a"},
			"carry with a later failing example planted on c1 that missed its fault":     {read: 106, refused: 936, digest: "f58d428fa1e0afefaacf58ec842e74f0dcae002dee3aeab88edabb70c9993b75"},
			"carry with a later failing example planted on c9, no commit of the history": {read: 106, refused: 936, digest: "008bb572bb5e26adc63254161f7fccdef391d25cdcefc0b3116440e60e33e7d1"},
			"carry with c2's failure at 10:30 and a later pass of m2":                    {read: 107, refused: 1024, digest: "eba195badd8041610d8bce83e087a2fc916bd9428c0b9c22ecf355dd8b7fc9a4"},
			"carry with c2's failure between c1's pass and c1's failing example":         {read: 107, refused: 1010, digest: "0ae1bf3e5952a16117ebb338ace796714d0a00dd63e30a212d2fe092ddbcadcf"},
			"carry with its failing example planted by no step":                          {read: 88, refused: 878, digest: "5f2347f53287856342a5a80e4adec19c8952ce723233f0743851cadc36d14822"},
			"earlier-text":           {read: 37, refused: 397, digest: "a2c49088505fc89ecbd1bc798ef11c10324f93f813648300ed4620d71a430bd5"},
			"one-commit-two-answers": {read: 68, refused: 724, digest: "649e81689007a5e8c6f62501b4c163fbb319091b4b31357e8293c3e60befab87"},
			"production-and-graph":   {read: 28, refused: 454, digest: "f027182d90e2857ad1e03cbbe05b3dc9b9e8d7568a6a07ca607d315c55950e03"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as a query of production":                 {read: 29, refused: 461, digest: "72bd478db7552f094034e95be6d1c6b54ecfeedd70aa85f4e7d0c4591e00e997"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent":                              {read: 29, refused: 461, digest: "bfa524ccb3b35feebb4ada9630ef7ae69e2a2074caf14700d783f194eca89e44"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent of production":                {read: 29, refused: 461, digest: "325449182e62f53583e92b8f6cca920be485f5b90594dc0ede44a9b341607f81"},
			"production-and-graph with CI's refusal at c1 of its query of the graph":                                          {read: 29, refused: 461, digest: "98f1b3d762aa892dd15c61bdc04c12da4900ec8b2c75703f13fe1c0ab07d0b2a"},
			"production-and-graph with its program check of production as a program check of the graph":                       {read: 28, refused: 454, digest: "6eb491861bdc5198185f290250b4c621905087135421dcf1c3567bf70ac8860d"},
			"production-and-graph with its program check of production as an agent of production":                             {read: 38, refused: 444, digest: "356b4b466c8862ca238cf1a2f9146fccb90025af2b8542132fa1c81d7fa3f014"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as a query of production":  {read: 30, refused: 456, digest: "a2a06d2b04b2209d5e767b333af0bd18de9a33219a43d0bf78b9e4d0896e498e"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent":               {read: 30, refused: 456, digest: "181e77d2346092c8982bdef9d78792046c774bf80f1efd44b860114a466cd472"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent of production": {read: 30, refused: 456, digest: "167ee510cbfd76c740a59a9d4448b78efc30e10c9f22419013fd80387db3b047"},
			"production-and-graph with test_commit c1 on the failing run of its query of the graph":                           {read: 30, refused: 456, digest: "60af2e2e09db0704bda9fd246ad66cae03d01bfebd5e397a574d8e0ead1c6a9d"},
			"refusal":    {read: 93, refused: 894, digest: "ed196ae140f6c8397b36dca7c9794ccf9d4adcf4571ebd8cbf54220f3bbc9f53"},
			"silent-leg": {read: 46, refused: 621, digest: "6ae42f77d812f03ac4f93ee58e73f14572755f0d633e82db6c58f10c08c5dd01"},
			"silent-leg with its matrix dropped at m2": {read: 46, refused: 605, digest: "b64aa2a3bcd7d90287018f75b628ad4712f45a5329f310effd8723eecb128d6b"},
			"wait-cycle": {read: 72, refused: 672, digest: "720438ca826856b01ec58bfb0a15a14d599d6407b8bdcf14363321f50d6216d5"},
			"wait-cycle with a second, passing check of a-message-survives-a-restart": {read: 92, refused: 815, digest: "4ffa282582ad3daccd4ff900ed5df442b7fee7a166bdb2c64cc3ebb8a5cddc40"},
			"wait-cycle with the outcomes of its two first runs swapped":              {read: 72, refused: 672, digest: "e4884195f65c5dd1dca3644530183a3bc6b7f0e5e0620e353ee25ee6160eac9f"},
		},
		"members": {
			"carry": {read: 103, refused: 22, digest: "4c063987f2aef7b6bd82f4a4027fddf1d1b08e5be90a4ba6ea9b0a3d1e0e77fb"},
			"carry with a failure of c0, c1's parent, and a later pass of c2":            {read: 193, refused: 33, digest: "168de5706dda3a9568853915c544cc1c8b4675388069b086084b3d882481397c"},
			"carry with a later failing example planted by no step":                      {read: 132, refused: 26, digest: "de854620007d40e51194813bc949cbdf64d0378aa0dd32ea81abe3794d274eaf"},
			"carry with a later failing example planted on c1 that missed its fault":     {read: 123, refused: 26, digest: "96a5f18fb59e3107d949af8aa614c5c7960d3c6974b1aa4c0c89568b476f0869"},
			"carry with a later failing example planted on c9, no commit of the history": {read: 132, refused: 27, digest: "49df4af355bab1bbb685ceb2a8110b7ad4e5887121075efc9136361d28856063"},
			"carry with c2's failure at 10:30 and a later pass of m2":                    {read: 180, refused: 32, digest: "cc2a5723769061d167461140dc84f47da14d088934ded73da0332d7f8d940f3d"},
			"carry with c2's failure between c1's pass and c1's failing example":         {read: 180, refused: 32, digest: "aaefdd39ae6ba0c7c887a72fa03db7c680889c9e3456d0d2620d5c2a3fa48828"},
			"carry with its failing example planted by no step":                          {read: 108, refused: 21, digest: "c0ee9e24d5cb0513980bdeb63484541e6e1388b1d21116474eff9371d43f4a72"},
			"earlier-text":           {read: 33, refused: 7, digest: "977dc4563c0a8b3186fce9bd7081d41f1042d4819f7d21a845d67d29026bf9be"},
			"one-commit-two-answers": {read: 150, refused: 21, digest: "02b11cc50f0bf2350812bf77da2b3a20925f7e37bec7ba7d678d6236a90bb24e"},
			"production-and-graph":   {read: 107, refused: 26, digest: "7412d2052a0779ae9acbeab4f10acf2d84de6a6f810ce48a287fcda328344f56"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as a query of production":                 {read: 107, refused: 26, digest: "5cb3ec1b198d24b6bf28cb65ad3167aca05c85cdde9d0a88d119607cea9f8461"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent":                              {read: 107, refused: 26, digest: "eac8c953865d41bf18039cfe7edc60916e90072a14b5b7162fcda59529547464"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent of production":                {read: 107, refused: 26, digest: "960b61ffedc9b181a50680ae399068d96d3b0b35048fa9bc6c19a0a245c46218"},
			"production-and-graph with CI's refusal at c1 of its query of the graph":                                          {read: 107, refused: 26, digest: "3b4c374b829b4f43fb064c10c92873cc55acece235bbc8c5d0b96ebb932090ff"},
			"production-and-graph with its program check of production as a program check of the graph":                       {read: 107, refused: 26, digest: "1ab78d703127366ca9ae605986940e6c4bd302fbae0ffe1efaa8491c279c5e0b"},
			"production-and-graph with its program check of production as an agent of production":                             {read: 107, refused: 26, digest: "bb0f5f89b30478d12e86491b736de64bec7bf8b62429524723e677868e2f2ffb"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as a query of production":  {read: 104, refused: 26, digest: "c6896f8cf7e4bc043c0adbf74e8fec17b06104ada79704877c5d5d93a2e0d5bf"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent":               {read: 104, refused: 26, digest: "e9c081fa6a6f01ff394065c8dce70ac77fc258c4eff08b6cdd7813fef7beae04"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent of production": {read: 104, refused: 26, digest: "6e610338b17d2d42eea2a0d96445b55afda5ca58c0bdf36012a1aad88d1fba58"},
			"production-and-graph with test_commit c1 on the failing run of its query of the graph":                           {read: 104, refused: 26, digest: "81312097ff37d43349bf4c198cd1d07bc284e48000040cbd9236e96ffb662c80"},
			"refusal":    {read: 103, refused: 22, digest: "9abb680a5efe997a896053da44b9273f385aa32f658c2c6c6f9328a5d41d0f9c"},
			"silent-leg": {read: 140, refused: 31, digest: "78be8c7d4f17d4f5cee9643a9dedcfd227e55520c0aee95b8e1894f5d1cc5430"},
			"silent-leg with its matrix dropped at m2": {read: 140, refused: 31, digest: "098086bcaed3b5a725cb3efc3e4c63019169b3ce3b1d72d4b6dfc2a7661eb880"},
			"wait-cycle": {read: 97, refused: 13, digest: "3535c37ded16e1f47bc120954f77259b93dd932feeae69f67cc5ed10d449f734"},
			"wait-cycle with a second, passing check of a-message-survives-a-restart": {read: 127, refused: 17, digest: "29b0bb9fe018adbd46a2235268fd0519454fcb4e7bce2acbe33a444074559fa3"},
			"wait-cycle with the outcomes of its two first runs swapped":              {read: 97, refused: 13, digest: "5288f09c06d90583c5d44e63be7555bd5e0aaff31da43db7fa8d13a988b4de3a"},
		},
		"texts": {
			"carry": {read: 201, refused: 374, digest: "6ee0d68c508710d36d9a078f29ae52bd013d49928c12719ec2f9c2756794ba21"},
			"carry with a failure of c0, c1's parent, and a later pass of c2":            {read: 201, refused: 374, digest: "94f36a9380443bae0db9e8833399a2c8ca402aeffb75d1c221dff9ee64f38994"},
			"carry with a later failing example planted by no step":                      {read: 201, refused: 374, digest: "cf784a0466c5b6d4a6999431c0363e67c00a25327c8b0f73ad6cc63db375a7e9"},
			"carry with a later failing example planted on c1 that missed its fault":     {read: 201, refused: 374, digest: "5f1da981b8d2d659989b7b8764387b68155ccd5210872f9f7ff086ff096113d1"},
			"carry with a later failing example planted on c9, no commit of the history": {read: 201, refused: 374, digest: "ad2822bc79c6906c4c965f22c8fc446565cdd36e64d08a697af2daa88c1695b8"},
			"carry with c2's failure at 10:30 and a later pass of m2":                    {read: 201, refused: 374, digest: "32427d5dcc459c74532d83b9d51858eae9bbdb4c7ec6d48157bd48ba37c6d139"},
			"carry with c2's failure between c1's pass and c1's failing example":         {read: 201, refused: 374, digest: "f122c9b9d8c679b64b6e8f56e6363ada92a14a6b5b25c4eb99f601b32d1d3e25"},
			"carry with its failing example planted by no step":                          {read: 201, refused: 374, digest: "562cd1d3dbe4787ac629a3ae779865de27fb770d531be9525bafc6ec68eebb39"},
			"earlier-text":           {read: 107, refused: 193, digest: "03e025818822aa1c4ddf2246881bf4725b29e6daea88e630bdf8e0912dacaf92"},
			"one-commit-two-answers": {read: 208, refused: 302, digest: "40f543b0d3332dcdfd10c98afa646621565468b97029c631e0d8ee3d5b96bc2a"},
			"production-and-graph":   {read: 77, refused: 129, digest: "e147043bdc55407195a48556fe547bab196309204d7e43ff9919cf3b3b71141b"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as a query of production":                 {read: 77, refused: 129, digest: "1a70e7f6de56444fb5a23d6374bc9bb262000aac633dafa3234ff1117d6cde08"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent":                              {read: 77, refused: 129, digest: "d3cc19cfe91c905c9135582c9109cdcf64e50a8ab750d3406199405c1b2086a6"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent of production":                {read: 77, refused: 129, digest: "015c0cbeb7546615246c3d30af242970b0930ef32501285d799e43d8f0064fb5"},
			"production-and-graph with CI's refusal at c1 of its query of the graph":                                          {read: 77, refused: 129, digest: "b7c33f1445cf3384ddd18e28347617916e973077b4c8065c9470ca9f67ce1f91"},
			"production-and-graph with its program check of production as a program check of the graph":                       {read: 77, refused: 129, digest: "18cef0afdf0d597c48c616cce2b0b53783293a92295f818f22b7e1a2d0cf4d94"},
			"production-and-graph with its program check of production as an agent of production":                             {read: 62, refused: 120, digest: "febda07a3b1670f4d30087c5d9ea73de3adb453726398dc0561637c6259e293d"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as a query of production":  {read: 77, refused: 129, digest: "d926dc0708e2973ad2470b95a985089b10f8bbdb51a55118918e60ab415ca15e"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent":               {read: 77, refused: 129, digest: "e64076ded5b0ae4288125a4ed514a94b7f7588ddd35b8ebdb7ea6185be7957cc"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent of production": {read: 77, refused: 129, digest: "29059fcaae3fe61181ccc02e74cacc68852d72356535fad30c272516bce9f1a3"},
			"production-and-graph with test_commit c1 on the failing run of its query of the graph":                           {read: 77, refused: 129, digest: "4282808cf5064d9b435b940fd4525ea40832687e0c50e37317229070154e1a55"},
			"refusal":    {read: 201, refused: 374, digest: "e0387726a07029e26e562c25a0f13866feed85b6ec3e5ea97c7330c3039cb973"},
			"silent-leg": {read: 125, refused: 220, digest: "2482e11c3e15b169e4cafd73c7570be9a311447679f463d7df41801fee8f0693"},
			"silent-leg with its matrix dropped at m2": {read: 125, refused: 220, digest: "9c82ce8c1ed47ff8eae76e444d69ed723f0a2501adebba2db3e4ff6b1517ca96"},
			"wait-cycle": {read: 149, refused: 201, digest: "9beb250396d4aceb4dc42c192effb143dbed3e6cd4a195c375ee986c162c121e"},
			"wait-cycle with a second, passing check of a-message-survives-a-restart": {read: 191, refused: 247, digest: "7a974c7c09ecc06440a62fb98ffc254fc35778078bf82c94b94b0c45124ddca2"},
			"wait-cycle with the outcomes of its two first runs swapped":              {read: 149, refused: 201, digest: "fb4542dbffeba7a426a00e52b6505c8071f35860c6ded13062fffc5712188769"},
		},
		"attributes": {
			"carry": {read: 10, refused: 16, digest: "a754b6ad39c4a1275187454f64a41eff4a60fe97ab593218c0d04c0d00d0c5b3"},
			"carry with a failure of c0, c1's parent, and a later pass of c2":            {read: 10, refused: 16, digest: "80ad1384f5a2dab822723f0c41df904e27c34eb96229e88083e5cfc67b743786"},
			"carry with a later failing example planted by no step":                      {read: 10, refused: 16, digest: "6dacb210fa71a12ced14099b997c73b764c0bfc7155a3a9715993c167ecc3efe"},
			"carry with a later failing example planted on c1 that missed its fault":     {read: 10, refused: 16, digest: "dda55d55cf78f219f8c0860d28966ccb706ffd938cc42785e9b01bdea5c721d8"},
			"carry with a later failing example planted on c9, no commit of the history": {read: 10, refused: 16, digest: "903cde0af4140ec34c802e53dd9ee93ba58713e6a47e9360e1d745136012f7cc"},
			"carry with c2's failure at 10:30 and a later pass of m2":                    {read: 10, refused: 16, digest: "e67ffc853d0b211c0632e4a38c71def0baf4bef29020801026f466b89efcce53"},
			"carry with c2's failure between c1's pass and c1's failing example":         {read: 10, refused: 16, digest: "cd6a50dedbef8b53a65b86ddc3b9764668d18d8199ff419503388624d8298428"},
			"carry with its failing example planted by no step":                          {read: 10, refused: 16, digest: "f129f94fe9409df83b95487e8133f3b83b65dbedb317b3d5f1e4de0204cab69e"},
			"earlier-text":           {read: 10, refused: 12, digest: "84a847d437ad4f3e031d487f33212007668c5a1693020c65469fe9bcf125b413"},
			"one-commit-two-answers": {read: 35, refused: 33, digest: "59cc50024db768d9818e471e663a46d65db351fc1e1c5af1bbe529a146d9685d"},
			"production-and-graph":   {read: 8, refused: 16, digest: "0a8adc6ae1938f3bcf455bfb047612092700d4067072f4f0bc7ebcd48efdd131"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as a query of production":                 {read: 8, refused: 16, digest: "6db2b234648192bf4177df24813331e81d84a384dfd0abeec65a0edb9479b908"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent":                              {read: 8, refused: 16, digest: "5c81094011c199ff88764acf443d570302300438b0b8e723b61a10c571b0afc8"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent of production":                {read: 8, refused: 16, digest: "19b32517046567c9585a4ca15d60e4aafa33550faa797851695a05f13f59b5bd"},
			"production-and-graph with CI's refusal at c1 of its query of the graph":                                          {read: 8, refused: 16, digest: "3f3d6b9f1b70006f6c88c63a32849588fccd20ef6c54532187351b16cf42ee0d"},
			"production-and-graph with its program check of production as a program check of the graph":                       {read: 8, refused: 16, digest: "d9144f9bdaa00a3b42b4b65914a542d06eba1f0b3f09c45fb5d0e6c9cc17797e"},
			"production-and-graph with its program check of production as an agent of production":                             {read: 5, refused: 13, digest: "e64b94cb0a69f71a6f3df680843eef5fff1cf88e2c506e99b1398fe370ef9bed"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as a query of production":  {read: 8, refused: 16, digest: "ed96945c65f0ffcbd50fb95bb56a5003feb410af2ec4d42e76dc56ab087e3bc5"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent":               {read: 8, refused: 16, digest: "dd2e0da30498778f0ec498b6e53c4cd2cd9af381c9bb359a3cab35b9e7fd094a"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent of production": {read: 8, refused: 16, digest: "3155fd4daabf7c40fc0db4afeac7a2c5142edbe4764eb3852f93daa9b16af16f"},
			"production-and-graph with test_commit c1 on the failing run of its query of the graph":                           {read: 8, refused: 16, digest: "32fc1914f05f46d7ac66ac3dfcb241fdb960d2261e666f97e5a54483d7d395fb"},
			"refusal":    {read: 10, refused: 16, digest: "c15d5c92d35f092efeb23ce8d1904766003610207f8711f1b5c94745f58bf266"},
			"silent-leg": {read: 10, refused: 16, digest: "701529d20affff9a12c40bfbd40bfe87bb402f30b3c9a10728496b12acb5a9fc"},
			"silent-leg with its matrix dropped at m2": {read: 10, refused: 16, digest: "6fff2ac80c7ddb46947565e4bd7cc5ca06057ed9623d723e960f49ade6c963f5"},
			"wait-cycle": {read: 22, refused: 24, digest: "c79fa6862baa997b24b8b5b0bcf4aa17675c2111cbfb22557f365ef756470a97"},
			"wait-cycle with a second, passing check of a-message-survives-a-restart": {read: 27, refused: 31, digest: "67b2d41b421e7b28fd12921d29990cabb106b5db1dd17db9cec6c1956ead9a84"},
			"wait-cycle with the outcomes of its two first runs swapped":              {read: 22, refused: 24, digest: "24b94c1b2afa55ebfc3e59ac11260f33b422ff9b35192ae5fa2df48334c73b05"},
		},
		"refusals": {
			"carry": {read: 14, refused: 0, digest: "05bae0ff9d95485348039d6531d940664d3481ebd0bb9beced5e236566ce52c6"},
			"carry with a failure of c0, c1's parent, and a later pass of c2":            {read: 18, refused: 0, digest: "1d99f2bdec1c811b5f028cf47f66fb6df8c0d9b3f459850b289d4806169b4106"},
			"carry with a later failing example planted by no step":                      {read: 16, refused: 0, digest: "b7a5ca12219b3614e7b978273288b41738b15ca6395a034b9420b0d0966cf411"},
			"carry with a later failing example planted on c1 that missed its fault":     {read: 16, refused: 0, digest: "2e62e462e5750f5661ac42953748da6d07cd67a8fcbed265edcca5384e3393e9"},
			"carry with a later failing example planted on c9, no commit of the history": {read: 18, refused: 0, digest: "7698a14bc33ac600403069210879351ecf6bebae7b3c56878dfdb917a632b9bf"},
			"carry with c2's failure at 10:30 and a later pass of m2":                    {read: 16, refused: 0, digest: "cde5956dd3528e845bd025391932a82176f67b26c1377cc45b04ba4f1c300850"},
			"carry with c2's failure between c1's pass and c1's failing example":         {read: 16, refused: 0, digest: "8facd6b11a43a01aace22aa7cadb7ef27e7cad53e02cc29d650570c5f665b09a"},
			"carry with its failing example planted by no step":                          {read: 12, refused: 0, digest: "478baaa0494c685b10808fff8f07f8dcc68b4ab95b69cfe5b264450603db5cc5"},
			"earlier-text":           {read: 5, refused: 0, digest: "897b6aba69a1d7475643554171a807438e639b26a4b14737bcd93a9aebe8b300"},
			"one-commit-two-answers": {read: 20, refused: 0, digest: "0526d52c18631885539e65d1e18030ac705dfd14120849f46593e41c9a3b411a"},
			"production-and-graph":   {read: 4, refused: 0, digest: "becdc88096164629fbf6c12eff5940036e41904df0bfffbeced390a2ba654187"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as a query of production":                 {read: 3, refused: 0, digest: "ec164bf1573538f590bb8da779e8fa793bf361f065aa1ae3bdd67d87d1329afa"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent":                              {read: 3, refused: 0, digest: "fb4157202c22f8e3e4d051a05dc4b553db82e7cc22797781d523339815ff5cdd"},
			"production-and-graph with CI's refusal at c1 of its check of the graph as an agent of production":                {read: 3, refused: 0, digest: "28dba7810f6be56ff98d4ebbb2769976158372c5f7ddcdd0bf8681622c6c940a"},
			"production-and-graph with CI's refusal at c1 of its query of the graph":                                          {read: 3, refused: 0, digest: "932bd6d15b39ca82cae70ef6ac3a1d853c89dfbabdb237c77ae7650d0b610cc5"},
			"production-and-graph with its program check of production as a program check of the graph":                       {read: 4, refused: 0, digest: "5bb8b9e0426f5016dc69810c674316974c3f069eebc3e8dfda37f0aa30b7259e"},
			"production-and-graph with its program check of production as an agent of production":                             {read: 4, refused: 0, digest: "3c32dbc3d1f7adc1a1f189f231c00f62f56a6d0dbd70e423a60c7e72ddb4aa82"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as a query of production":  {read: 4, refused: 0, digest: "60a990083285811fe77736e0c72aef83e13dead69565b52f92cf8326420e7fa5"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent":               {read: 4, refused: 0, digest: "44a97a4672c9d183269b76ae9df5723f8dd18180851278baed6f46c51291a731"},
			"production-and-graph with test_commit c1 on the failing run of its check of the graph as an agent of production": {read: 4, refused: 0, digest: "b3f6a68c90d216998b0c36bf752107179a9827e00361d0f761b4ec752e9a40c3"},
			"production-and-graph with test_commit c1 on the failing run of its query of the graph":                           {read: 4, refused: 0, digest: "b0bc4434eb58ce840e011bafad0c58f43f7c04de726f6e888ea3d365ec92c6ac"},
			"refusal":    {read: 13, refused: 0, digest: "b5c090908a245c23ab6c65738f593eead2fcfda33f7643db2134616be3920313"},
			"silent-leg": {read: 10, refused: 0, digest: "b0b1978953183b52e369927555886119b0e299483f23097d9a205d3a6fbfce2f"},
			"silent-leg with its matrix dropped at m2": {read: 10, refused: 0, digest: "d08a86ae09156d29e337a4a17bea2e14f7970bffab547ba0a93f6db03b5e603c"},
			"wait-cycle": {read: 18, refused: 0, digest: "6c6ea28d7896113b4943e7a32dfe2c7e2214b0645d084defc9b6cf2ad9057a92"},
			"wait-cycle with a second, passing check of a-message-survives-a-restart": {read: 24, refused: 0, digest: "d3faf5605b3a6cfda641ade43a6da52ea8b2d4bce7bb14bfa7f6ec920d63b60e"},
			"wait-cycle with the outcomes of its two first runs swapped":              {read: 18, refused: 0, digest: "df405f6fb508970235fccda06011e49908edc9f0b215247ab094cad3629ee8b9"},
		},
	}
}

// sweepFloor is the floor of one sweep of one base: the inputs that the reader read, as the base or
// as another state, and the inputs that it refused, as sweepAgainst counted the inputs that it
// judged (sweepCount), and digest, the hex sha256 of sweepCount.judged: the key of the base's files,
// then each judged input's key (inputKey, its files apart from the base's, by name and bytes) and
// kind, one byte, 'r' read and 'x' refused, in their order.
type sweepFloor struct {
	read, refused int
	digest        string
}

// markReadingsFloor is the count of readings of a member left out that
// TestAMemberLeftOutOfAFileIsReadByItsMark read when the floor was written.
const markReadingsFloor = 82

// atFloor fails the test unless, of the inputs of the base name that sweepAgainst judged (n), the
// reader read at least the floor's count and refused at least the floor's count, and the base's
// files and the files and kinds of those inputs, in their order, have the floor's digest
// (sweepFloors). It counts no input that the sweep built and did not judge.
func atFloor(t *testing.T, sweep, name string, n sweepCount) {
	t.Helper()
	floor, held := sweepFloors()[sweep][name]
	if read := n.same + n.moved; !held || read < floor.read || n.refused < floor.refused {
		t.Errorf("the %s sweep judged %d inputs of %s that the reader read and %d that it refused, and its floor is %d read and %d refused (held %t; sweepFloors)",
			sweep, read, name, n.refused, floor.read, floor.refused, held)
	}
	if digest := fmt.Sprintf("%x", sha256.Sum256(n.judged)); held && digest != floor.digest {
		t.Errorf("the %s sweep judged the %d inputs of %s, in their order, as files and kinds whose digest is %s, and its floor's digest is %s (sweepFloors): the base's files or an input's files changed, an input moved from one kind to the other, or the sweep judged other inputs",
			sweep, n.same+n.moved+n.refused, name, digest, floor.digest)
	}
}

// floorsCover fails the test unless the sweep has a floor of inputs for each base of bases and for
// no other (sweepFloors).
func floorsCover(t *testing.T, sweep string, bases map[string]fstest.MapFS) {
	t.Helper()
	if got, want := slices.Sorted(maps.Keys(sweepFloors()[sweep])), slices.Sorted(maps.Keys(bases)); !slices.Equal(got, want) {
		t.Errorf("the %s sweep has floors of inputs for the bases %q, and the sweep reads the bases %q (sweepFloors)", sweep, got, want)
	}
}
