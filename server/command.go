package main

import (
	"fmt"
	"strings"

	"github.com/mattermost/mattermost-server/v5/model"
	"github.com/mattermost/mattermost-server/v5/plugin"
)

func getHelp() string {
	return `Available Commands:

add [message]
	Adds a Todo.

	example: /todo add Don't forget to be awesome

list
	Lists your Todo issues.

list [listName]
	List your issues in certain list

	example: /todo list in
	example: /todo list out
	example (same as /todo list): /todo list my

pop
	Removes the Todo issue at the top of the list.

send [user] [message]
	Sends some user a Todo

	example: /todo send @awesomePerson Don't forget to be awesome

help
	Display usage.
`
}

func getCommand() *model.Command {
	return &model.Command{
		Trigger:          "todo",
		DisplayName:      "Todo Bot",
		Description:      "Interact with your Todo list.",
		AutoComplete:     true,
		AutoCompleteDesc: "Available commands: add, list, pop",
		AutoCompleteHint: "[command]",
	}
}

func (p *Plugin) postCommandResponse(args *model.CommandArgs, text string) {
	post := &model.Post{
		UserId:    p.BotUserID,
		ChannelId: args.ChannelId,
		Message:   text,
	}
	_ = p.API.SendEphemeralPost(args.UserId, post)
}

func (p *Plugin) getCommandResponse(args *model.CommandArgs, text string) *model.CommandResponse {
	p.postCommandResponse(args, text)
	return &model.CommandResponse{}
}

// ExecuteCommand executes a given command and returns a command response.
func (p *Plugin) ExecuteCommand(c *plugin.Context, args *model.CommandArgs) (*model.CommandResponse, *model.AppError) {
	stringArgs := strings.Split(strings.TrimSpace(args.Command), " ")
	lengthOfArgs := len(stringArgs)
	restOfArgs := []string{}

	var handler func([]string, *model.CommandArgs) (*model.CommandResponse, bool, error)
	if lengthOfArgs == 1 {
		handler = p.runListCommand
	} else {
		command := stringArgs[1]
		if lengthOfArgs > 2 {
			restOfArgs = stringArgs[2:]
		}
		switch command {
		case "add":
			handler = p.runAddCommand
		case "list":
			handler = p.runListCommand
		case "pop":
			handler = p.runPopCommand
		case "send":
			handler = p.runSendCommand
		default:
			return p.getCommandResponse(args, getHelp()), nil
		}
	}
	resp, isUserError, err := handler(restOfArgs, args)
	if err != nil {
		if isUserError {
			return p.getCommandResponse(args, fmt.Sprintf("__Error: %s__\n\nRun `/todo help` for usage instructions.", err.Error())), nil
		}
		p.API.LogError(err.Error())
		return p.getCommandResponse(args, "An unknown error occurred. Please talk to your system administrator for help."), nil
	}

	return resp, nil
}

func (p *Plugin) runSendCommand(args []string, extra *model.CommandArgs) (*model.CommandResponse, bool, error) {
	if len(args) < 2 {
		return p.getCommandResponse(extra, "You must specify a user and a message.\n"+getHelp()), false, nil
	}

	userName := args[0]
	if args[0][0] == '@' {
		userName = args[0][1:]
	}
	receiver, appErr := p.API.GetUserByUsername(userName)
	if appErr != nil {
		return p.getCommandResponse(extra, "Please, provide a valid user.\n"+getHelp()), false, nil
	}

	if receiver.Id == extra.UserId {
		return p.runAddCommand(args[1:], extra)
	}

	message := strings.Join(args[1:], " ")

	receiverIssueID, err := p.listManager.SendIssue(extra.UserId, receiver.Id, message, "")
	if err != nil {
		return nil, false, err
	}

	p.sendRefreshEvent(extra.UserId)
	p.sendRefreshEvent(receiver.Id)

	responseMessage := fmt.Sprintf("Todo sent to @%s.", userName)

	senderName := p.listManager.GetUserName(extra.UserId)

	receiverMessage := fmt.Sprintf("You have received a new Todo from @%s", senderName)

	p.PostBotCustomDM(receiver.Id, receiverMessage, message, receiverIssueID)
	return p.getCommandResponse(extra, responseMessage), false, nil
}

func (p *Plugin) runAddCommand(args []string, extra *model.CommandArgs) (*model.CommandResponse, bool, error) {
	message := strings.Join(args, " ")

	if message == "" {
		return p.getCommandResponse(extra, "Please add a task."), false, nil
	}

	if err := p.listManager.AddIssue(extra.UserId, message, ""); err != nil {
		return nil, false, err
	}

	p.sendRefreshEvent(extra.UserId)

	responseMessage := "Added Todo."

	issues, err := p.listManager.GetIssueList(extra.UserId, MyListKey)
	if err != nil {
		p.API.LogError(err.Error())
		return p.getCommandResponse(extra, responseMessage), false, nil
	}

	responseMessage += "Todo List:\n\n"
	responseMessage += issuesListToString(issues)

	return p.getCommandResponse(extra, responseMessage), false, nil
}

func (p *Plugin) runListCommand(args []string, extra *model.CommandArgs) (*model.CommandResponse, bool, error) {
	listID := MyListKey
	responseMessage := "Todo List:\n\n"

	if len(args) > 0 {
		switch args[0] {
		case "my":
		case "in":
			listID = InListKey
			responseMessage = "Received Todo list:\n\n"
		case "out":
			listID = OutListKey
			responseMessage = "Sent Todo list:\n\n"
		default:
			return p.getCommandResponse(extra, getHelp()), true, nil
		}
	}

	issues, err := p.listManager.GetIssueList(extra.UserId, listID)
	if err != nil {
		return nil, false, err
	}
	p.sendRefreshEvent(extra.UserId)

	responseMessage += issuesListToString(issues)

	return p.getCommandResponse(extra, responseMessage), false, nil
}

func (p *Plugin) runPopCommand(args []string, extra *model.CommandArgs) (*model.CommandResponse, bool, error) {
	issue, err := p.listManager.PopIssue(extra.UserId)
	if err != nil {
		return nil, false, err
	}

	userName := p.listManager.GetUserName(extra.UserId)

	if issue.ForeignUser != "" {
		message := fmt.Sprintf("@%s popped a Todo you sent: %s", userName, issue.Message)
		p.sendRefreshEvent(issue.ForeignUser)
		p.PostBotDM(issue.ForeignUser, message)
	}

	p.sendRefreshEvent(extra.UserId)

	responseMessage := "Removed top Todo."

	replyMessage := fmt.Sprintf("@%s popped a todo attached to this thread", userName)
	p.postReplyIfNeeded(issue.PostID, replyMessage, issue.Message)

	issues, err := p.listManager.GetIssueList(extra.UserId, MyListKey)
	if err != nil {
		p.API.LogError(err.Error())
		return p.getCommandResponse(extra, responseMessage), false, nil
	}

	responseMessage += "Todo List:\n\n"
	responseMessage += issuesListToString(issues)

	return p.getCommandResponse(extra, responseMessage), false, nil
}
