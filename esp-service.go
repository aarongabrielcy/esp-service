package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api"
	"github.com/gorilla/websocket"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

var mongoClient *mongo.Client
var collection *mongo.Collection

const (
	minTemp       = 25.0
	maxTemp       = 35.0
	alertDuration = 1 * time.Minute
)

var tempMonitor = make(map[string]time.Time)

func initMongoDB() {
	// Configura la conexión a MongoDB
	clientOptions := options.Client().ApplyURI("mongodb://dbadmin:Linkzero23.@localhost:27017")
	var err error
	mongoClient, err = mongo.Connect(context.TODO(), clientOptions)
	if err != nil {
		log.Fatal(err)
	}

	// Verifica la conexión
	err = mongoClient.Ping(context.TODO(), nil)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("Conectado a MongoDB!")
	collection = mongoClient.Database("trackingSensors").Collection("sensorData")
}

type SensorData struct {
	SensorData string `json:"sensor_data"`
}

var clients = make(map[*websocket.Conn]bool)
var broadcast = make(chan map[string]interface{})
var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// Función para configurar manualmente los encabezados CORS
func setCorsHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "http://localhost:8080")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

func decodeManufacturerData(data string) map[string]interface{} {
	regex := regexp.MustCompile(`^(?P<trackingHead>[A-z]{3,8});(?P<imei>[a-zA-Z0-9]{12});(?P<sensorMac>[a-zA-Z0-9]{12});(?P<company>[a-zA-Z0-9]{4})(?P<protocol>[0-9]{2})(?P<flags>[a-zA-Z0-9]{2})(?P<temperature>[a-zA-Z0-9]{4})(?P<humidity>[a-zA-Z0-9]{2})(?P<movement>[a-zA-Z0-9]{4})(?P<angle>[a-zA-Z0-9]{6})(?P<battery>[a-zA-Z0-9]{2})$`)
	match := regex.FindStringSubmatch(data)
	if match == nil {
		log.Printf("Data does not match the expected format: %s\n", data)
	}

	result := make(map[string]interface{})
	for i, name := range regex.SubexpNames() {
		if i != 0 && name != "" {
			value := match[i]
			switch name {
			case "flags":
				if valueBytes, err := hex.DecodeString(value); err == nil {
					result[name] = fmt.Sprintf("%08b", valueBytes[0])
				}
			case "temperature":
				if temp, err := strconv.ParseInt(value, 16, 32); err == nil {
					result[name] = float64(temp) / 100.0
					monitorTemperature(value, result)
				}
			case "humidity":
				if hum, err := strconv.ParseInt(value, 16, 32); err == nil {
					result[name] = float64(hum) / 2.0
				}
			case "movement":
				if moveBytes, err := hex.DecodeString(value); err == nil {
					result[name] = fmt.Sprintf("%08b%08b", moveBytes[0], moveBytes[1])
				}
			case "angle":
				if angleBytes, err := hex.DecodeString(value); err == nil {
					result[name] = fmt.Sprintf("%08b%08b%08b", angleBytes[0], angleBytes[1], angleBytes[2])
				}
			case "battery":
				if batt, err := strconv.ParseInt(value, 16, 64); err == nil {
					result[name] = (2000 + (float64(batt) * 10)) / 1000
				}
			case "company":
				if comp, err := strconv.ParseInt(value, 16, 32); err == nil {
					result[name] = comp
				}
			case "protocol":
				if proto, err := strconv.ParseInt(value, 16, 32); err == nil {
					result[name] = proto
				}
			case "sensorMac":
				result[name] = value
			default:
				result[name] = value
			}
		}
	}

	// Añadir timestamp
	result["timestamp"] = time.Now()
	document := bson.D{}
	for key, value := range result {
		document = append(document, bson.E{Key: key, Value: value})
	}
	_, err := collection.InsertOne(context.TODO(), document)
	if err != nil {
		log.Printf("Error inserting document into MongoDB: %v\n", err)
	} else {
		fmt.Println("Datos guardados en MongoDB:", result)
	}
	return result
}
func monitorTemperature(temp string, data map[string]interface{}) {
	fmt.Println("Temperatura String: ", temp)

	militemp, _ := strconv.ParseInt(temp, 16, 32)
	temperature := float64(militemp) / 100.0
	sensorMac := data["sensorMac"].(string)

	if temperature < minTemp || temperature > maxTemp {
		fmt.Println("Activa alerta de temperatura: ", temperature)
		if _, ok := tempMonitor[sensorMac]; !ok {
			tempMonitor[sensorMac] = time.Now()
		} else if time.Since(tempMonitor[sensorMac]) > alertDuration {
			sendAlertToTelegram(data)
			delete(tempMonitor, sensorMac)
		}

	} else {
		fmt.Println("La temperatura está en su rango: ", temperature)
		delete(tempMonitor, sensorMac)

	}
}

func sendAlertToTelegram(data map[string]interface{}) {
	botToken := "7246662165:AAHdPT1lb7rZ7u-SwVRUF_iDP6L2B8Ddhdw"
	chatID := int64(895317032)

	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		log.Panic(err)
	}

	alertMessage := fmt.Sprintf("Alerta de temperatura:\n\n%s", formatSensorData(data))
	msg := tgbotapi.NewMessage(chatID, alertMessage)

	_, err = bot.Send(msg)
	if err != nil {
		log.Printf("Error sending alert to Telegram: %v", err)
	}
}
func formatSensorData(data map[string]interface{}) string {
	result := ""
	for key, value := range data {
		result += fmt.Sprintf("%s: %v\n", key, value)
	}
	return result
}
func handler(w http.ResponseWriter, r *http.Request) {
	setCorsHeaders(w)
	if r.Method == http.MethodPost {
		var data SensorData
		err := json.NewDecoder(r.Body).Decode(&data)
		if err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		fmt.Println("Received data:", data.SensorData)

		decodedData := decodeManufacturerData(data.SensorData)
		if decodedData == nil {
			http.Error(w, "Invalid Manufacturer Data Format", http.StatusBadRequest)
			return
		}

		fmt.Println("Decoded Manufacturer Data:")
		for key, value := range decodedData {
			fmt.Printf("%s: %v\n", key, value)
		}

		broadcast <- decodedData

		w.WriteHeader(http.StatusOK)
		return
	} else if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
}

func getSensorData(w http.ResponseWriter, r *http.Request) {
	setCorsHeaders(w)

	filter := bson.D{}
	findOptions := options.Find().SetSort(bson.D{{Key: "timestamp", Value: -1}}).SetLimit(5)

	cur, err := collection.Find(context.TODO(), filter, findOptions)
	if err != nil {
		http.Error(w, "Error al obtener los datos", http.StatusInternalServerError)
		return
	}
	defer cur.Close(context.TODO())

	var results []bson.M
	if err = cur.All(context.TODO(), &results); err != nil {
		http.Error(w, "Error al procesar los datos", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

func handleConnections(w http.ResponseWriter, r *http.Request) {
	setCorsHeaders(w)
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer ws.Close()

	clients[ws] = true

	for {
		var msg map[string]interface{}
		err := ws.ReadJSON(&msg)
		if err != nil {
			log.Printf("error: %v", err)
			delete(clients, ws)
			break
		}
	}
}

func handleMessages() {
	for {
		msg := <-broadcast
		for client := range clients {
			err := client.WriteJSON(msg)
			if err != nil {
				log.Printf("error: %v", err)
				client.Close()
				delete(clients, client)
			}
		}
	}
}

func main() {
	// Inicializa MongoDB
	initMongoDB()

	http.HandleFunc("/endpoint", handler)
	http.HandleFunc("/sensor-data", getSensorData)
	http.HandleFunc("/ws/", handleConnections)

	go handleMessages()

	port := "8081"
	ipAddress := "localhost"

	addrs, err := net.InterfaceAddrs()
	if err != nil {
		log.Fatalf("Failed to get network interfaces: %v", err)
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && ipnet.IP.IsGlobalUnicast() {
			ipAddress = ipnet.IP.String()
			break
		}
	}

	log.Printf("Starting server on %s:%s", ipAddress, port)
	err = http.ListenAndServe(ipAddress+":"+port, nil)
	if err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
