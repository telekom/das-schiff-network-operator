<!--

(c) Karsten Reincke, Deutsche Telekom AG

This file is distributed under the terms of the creative commons license CC0. For details see https://creativecommons.org/publicdomain/zero/1.0/

Deutsche Telekom makes no warranties about the work, and disclaims liability for all uses of the work, to the fullest extent permitted by applicable law.

In accordance with the CC0 license feel free to erase this header.

-->
# OSPOID: Case Title

* *ID: OSPOID*
* *Type: Specified BOM of [Dev. Tools | Server | Client]*


> In the specific compliance case file you should already have documented the software architecture of your system / product you want to distribute / sell compliantly. Now you must create the respective **qualified BOMs** (= Bill of Materials)

> * _Creating a preparatory **unspecified BOM**_ means: *For each component of your system create a list of all used open source applications and all embedded open source libraries / modules / snippets* . The best way to do so is filling the ospo-nus-bom.csv which uses the structure `ComponentName;ReleaseNumber;CType={app,lib};PStat={rap,unm,mod};License;` . If you use this format, you can use the script `nusbom2sbom.sh` for initializing a specified BOM with the data you've already gathered.

> * **Creating a specified BOM** means *For each entry of each __BOM__ find and document the respective licensing statements by inserting into the respective line of the list*
  - CName = ComponentName
  - HPUrl = link to the respective homepage
  - RelNr = the respective release number (helpful)
  - CType = type of the component = { `app` [= autonomously running process in its own address stack], `dll` [= dynamically linked lib], `sll` [= statically linked lib], `ics` ]= included code snippet] }
  - PStat = { `rap` [= required as preinstalled component], unm [= unmodified FOSS component has been integrated into the distributable package], mod [= modified FOSS component has been integrated into the distributable package]  }
  - RepoUrl = the link to the source code repository
  - SpdxID = License-Identifier in accordance with https://spdx.org/licenses/
  - LTUrl = link to the respective license file or licensing statement
  - LTType = type of the license-text. It can be a standalone license text (stal), a license text embedded into an overarching document like a README (embl) only a licensing statement without the license text itself (olst)


|NO|\[CName\](HpUrl)|RelNr|CType = {app, dll, sll, ics}|PStat = {rap, unm, mod}|\[RepoUrl\](RepoUrl)|\[SpdxId\](LTUrl)|LTType = {stal, embl, olst}|NFile = {irr, no, \[yes](NFUrl)}|
|---|---|---|---|---|---|---|---|---|
|0|[Bottleneck](https://pypi.org/project/Bottleneck/)|1.3.2|lib|unm|[https://github.com/pydata/bottleneck](https://github.com/pydata/bottleneck)|[BSD-2-Clause](https://github.com/pydata/bottleneck/blob/master/LICENSE)|stal|irr|
|1|[]()|?.?.?|dll|unm|[]()|[]()|stal|irr|

---
![CC0](cc-zero.png)

(C) 2021 Karsten Reincke, Deutsche Telekom AG: This file is distributed under the terms of the [CC0-license](https://creativecommons.org/publicdomain/zero/1.0/)

Deutsche Telekom makes no warranties about the work, and disclaims liability for all uses of the work, to the fullest extent permitted by applicable law.
