// See LICENSE.txt in the repository root for license information.

package main

import "github.com/mattermost/mattermost/server/public/plugin"

func main() {
	plugin.ClientMain(&Plugin{})
}
